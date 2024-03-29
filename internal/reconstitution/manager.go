package reconstitution

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// New creates a new Manager, which is responsible for "reconstituting" resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (ResourceSlice).
func New(mgr ctrl.Manager, rec Reconciler) (*Manager, error) {
	m := &Manager{
		Manager: mgr,
	}

	var err error
	m.reconstituter, err = newReconstituter(mgr)
	if err != nil {
		return nil, err
	}

	qp := &queueProcessor{
		Client:  m.Manager.GetClient(),
		Queue:   m.reconstituter.queue,
		Recon:   m.reconstituter,
		Handler: rec,
		Logger:  m.Manager.GetLogger().WithValues("controller", "reconciliationController"),
	}
	mgr.Add(qp)

	return m, nil
}

type Manager struct {
	ctrl.Manager
	*reconstituter
}

func (m *Manager) GetClient() Client { return m }

type queueProcessor struct {
	Client  client.Client
	Queue   workqueue.RateLimitingInterface
	Recon   *reconstituter
	Handler Reconciler
	Logger  logr.Logger
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

	req, ok := item.(*Request)
	if !ok {
		q.Logger.Error(nil, "failed type assertion in queue processor")
		return false
	}

	logger := q.Logger.WithValues("compositionName", req.Composition.Name, "compositionNamespace", req.Composition.Namespace, "resourceKind", req.Resource.Kind, "resourceName", req.Resource.Name, "resourceNamespace", req.Resource.Namespace)
	ctx = logr.NewContext(ctx, logger)

	result, err := q.Handler.Reconcile(ctx, req)
	if err != nil {
		q.Queue.AddRateLimited(item)
		logger.Error(err, "error while processing queue item")
		return true
	}
	if result.Requeue {
		q.Queue.Forget(item) // TODO: Maybe omit after first retry to avoid getting stuck in a patch tightloop?
		q.Queue.Add(item)
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
