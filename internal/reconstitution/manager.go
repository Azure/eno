package reconstitution

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
)

var ErrNotFound = errors.New("resource not found")

type Reconciler interface {
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

type Client interface {
	Get(ctx context.Context, gen int64, req *GeneratedResourceMeta) (*GeneratedResource, error)
	ObserveResource(ctx context.Context, req *Request, gen int64, resourceVersion string) error
}

// New creates a new Manager, which is responsible for "reconstituting" generated resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (GeneratedResourceSlice).
func New(mgr ctrl.Manager) (*Manager, error) {
	m := &Manager{
		Manager: mgr,
		recon: &reconstituter{
			Client:                       mgr.GetClient(),
			resources:                    make(map[resourceKey]*GeneratedResource),
			attemptsByGeneration:         make(map[types.NamespacedName][]int64),
			resourcesByGenerationAttempt: map[generationKey][]resourceKey{},
		},
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.Generation{}).
		Owns(&apiv1.GeneratedResourceSlice{}).
		Build(m.recon)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

type Manager struct {
	ctrl.Manager
	recon *reconstituter
}

func (m *Manager) GetClient() Client { return m.recon }

func (m *Manager) Add(rec Reconciler) error {
	rateLimiter := workqueue.DefaultControllerRateLimiter()
	queue := workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{
		Name: "generatedResourceReconciler",
	})
	qp := &queueProcessor{
		Client:  m.Manager.GetClient(),
		Queue:   queue,
		Recon:   m.recon,
		Handler: rec,
	}
	m.recon.Queues = append(m.recon.Queues, qp.Queue)
	return m.Manager.Add(qp)
}
