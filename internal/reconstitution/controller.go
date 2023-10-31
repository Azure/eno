package reconstitution

import (
	"context"
	"errors"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// TODO: Resolve secret references

var ErrNotFound = errors.New("resource not found")

type GeneratedResourceReq struct {
	Name, Namespace, Kind string
}

type GeneratedResource struct {
	Spec   apiv1.GeneratedResourceSpec
	Status apiv1.GeneratedResourceStatus

	// TODO: Populate these values
	Parsed           *unstructured.Unstructured
	PreviousManifest string
	Removed          bool
}

type Reconciler interface {
	Reconcile(ctx context.Context, req *GeneratedResourceReq) (ctrl.Result, error)
}

type Client interface {
	Get(ctx context.Context, req *GeneratedResourceReq) (*GeneratedResource, error)
	UpdateStatus(context.Context, *GeneratedResourceReq, *apiv1.GeneratedResourceStatus) error
}

// New creates a new Manager, which is responsible for "reconstituting" generated resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (GeneratedResourceSlice).
func New(mgr ctrl.Manager) (*Manager, error) {
	m := &Manager{
		Manager: mgr,
		recon: &reconstituter{
			Client: mgr.GetClient(),
		},
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.GeneratedResourceSlice{}).
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

type queueProcessor struct {
	Client  client.Client
	Queue   workqueue.RateLimitingInterface
	Recon   *reconstituter
	Handler Reconciler
}

func (q *queueProcessor) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		q.Queue.ShutDown()
	}()
	for q.processQueueItem(ctx) {
	}
	return nil
}

func (q *queueProcessor) processQueueItem(ctx context.Context) bool {
	item, shutdown := q.Queue.Get()
	if shutdown {
		return false
	}
	defer q.Queue.Done(item)

	req := item.(*GeneratedResourceReq)
	result, err := q.Handler.Reconcile(ctx, req)
	if err != nil {
		q.Queue.AddRateLimited(item)
		return true
	}
	if result.RequeueAfter != 0 {
		q.Queue.Forget(item)
		q.Queue.AddAfter(item, result.RequeueAfter)
		return true
	}

	q.Queue.Forget(item)
	return true
}

type reconstituter struct {
	Client client.Client
	Queues []workqueue.Interface

	mut   sync.Mutex
	byReq map[GeneratedResourceReq]*GeneratedResource
}

func (r *reconstituter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	slice := &apiv1.GeneratedResourceSlice{}
	err := r.Client.Get(ctx, req.NamespacedName, slice)
	if err != nil {
		return ctrl.Result{}, err
	}

	// TODO: Parse, cache, push to queue while handling the possibility of multiple versions

	return ctrl.Result{}, nil
}

func (r *reconstituter) Get(ctx context.Context, req *GeneratedResourceReq) (*GeneratedResource, error) {
	r.mut.Lock()
	defer r.mut.Unlock()

	res, ok := r.byReq[*req]
	if !ok {
		return nil, ErrNotFound
	}
	return res, nil
}

func (r *reconstituter) UpdateStatus(context.Context, *GeneratedResourceReq, *apiv1.GeneratedResourceStatus) error {
	return nil // TODO: Use work queue for batching? Re-enqueue in main queue on failure/conflict to retry, add slice resource version private to req
}
