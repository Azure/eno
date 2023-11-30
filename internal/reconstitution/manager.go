package reconstitution

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// New creates a new Manager, which is responsible for "reconstituting" resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (ResourceSlice).
func New(mgr ctrl.Manager, writeBatchInterval time.Duration) (Manager, error) {
	m := &reconcilerManager{
		Manager: mgr,
	}
	m.writeBuffer = newWriteBuffer(mgr.GetClient(), writeBatchInterval, 1)
	mgr.Add(m.writeBuffer)

	var err error
	m.reconstituter, err = newReconstituter(mgr)
	if err != nil {
		return nil, err
	}

	return m, nil
}

type Manager interface {
	GetClient() Client
	Add(rec Reconciler) error
}

type reconcilerManager struct {
	ctrl.Manager
	*reconstituter
	*writeBuffer
}

func (m *reconcilerManager) GetClient() Client { return m }

func (m *reconcilerManager) Add(rec Reconciler) error {
	rateLimiter := workqueue.DefaultControllerRateLimiter()
	queue := workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{
		Name: rec.Name(),
	})
	qp := &queueProcessor{
		Client:        m.Manager.GetClient(),
		Queue:         queue,
		Reconstituter: m.reconstituter,
		Handler:       rec,
		Logger:        m.Manager.GetLogger().WithValues("controller", rec.Name()),
	}
	m.reconstituter.AddQueue(queue)
	return m.Manager.Add(qp)
}

type queueProcessor struct {
	Client        client.Client
	Queue         workqueue.RateLimitingInterface
	Reconstituter *reconstituter
	Handler       Reconciler
	Logger        logr.Logger
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

	logger := q.Logger.WithValues("compositionName", req.Composition.Name, "compositionNamespace", req.Composition.Namespace, "resourceKind", req.ResourceRef.Kind, "resourceName", req.ResourceRef.Name, "resourceNamespace", req.ResourceRef.Namespace)
	ctx = logr.NewContext(ctx, logger)

	result, err := q.Handler.Reconcile(ctx, req)
	if err != nil {
		q.Queue.AddRateLimited(item)
		logger.Error(err, "error while processing queue item")
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
