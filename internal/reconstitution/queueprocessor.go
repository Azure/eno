package reconstitution

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

	req := item.(*Request)
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
