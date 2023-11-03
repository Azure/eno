package reconstitution

import (
	"context"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// New creates a new Manager, which is responsible for "reconstituting" resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (ResourceSlice).
func New(mgr ctrl.Manager, writeBatchInterval time.Duration) (*Manager, error) {
	m := &Manager{
		Manager: mgr,
		reconstituter: &reconstituter{
			Client:                 mgr.GetClient(),
			resources:              make(map[resourceKey]*Resource),
			Logger:                 mgr.GetLogger().WithName("reconstituter"),
			synthesesByComposition: make(map[types.NamespacedName][]int64),
			resourcesBySynthesis:   map[synthesisKey][]resourceKey{},
		},
	}
	m.writeBuffer = newWriteBuffer(mgr.GetClient(), mgr.GetLogger(), writeBatchInterval)
	mgr.Add(m.writeBuffer)

	err := mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, "spec.compositionGeneration", func(o client.Object) []string {
		slice := o.(*apiv1.ResourceSlice)
		return []string{strconv.FormatInt(slice.Spec.CompositionGeneration, 10)}
	})
	if err != nil {
		return nil, err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, "metadata.ownerReferences.name", func(o client.Object) (keys []string) {
		slice := o.(*apiv1.ResourceSlice)
		for _, owner := range slice.OwnerReferences {
			if owner.Kind == "Composition" {
				keys = append(keys, owner.Name)
			}
		}
		return keys
	})
	if err != nil {
		return nil, err
	}

	_, err = ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		Build(m.reconstituter)
	if err != nil {
		return nil, err
	}

	return m, nil
}

// TODO: Don't expose all public methods
type Manager struct {
	ctrl.Manager
	*reconstituter
	*writeBuffer
}

func (m *Manager) GetClient() Client { return m }

func (m *Manager) Add(rec Reconciler) error {
	rateLimiter := workqueue.DefaultControllerRateLimiter()
	queue := workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{
		Name: rec.Name(),
	})
	qp := &queueProcessor{
		Client:  m.Manager.GetClient(),
		Queue:   queue,
		Recon:   m.reconstituter,
		Handler: rec,
		Logger:  m.Manager.GetLogger().WithValues("controller", rec.Name()),
	}
	m.reconstituter.Queues = append(m.reconstituter.Queues, qp.Queue)
	return m.Manager.Add(qp)
}
