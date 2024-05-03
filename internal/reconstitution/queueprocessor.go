package reconstitution

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
)

type queueProcessor struct {
	Queue   workqueue.RateLimitingInterface
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

	req, ok := item.(Request)
	if !ok {
		q.Logger.Error(nil, "failed type assertion in queue processor")
		return false
	}

	logger := q.Logger.WithValues("compositionName", req.Composition.Name, "compositionNamespace", req.Composition.Namespace, "resourceKind", req.Resource.Kind, "resourceName", req.Resource.Name, "resourceNamespace", req.Resource.Namespace)
	ctx = logr.NewContext(ctx, logger)

	result, err := q.Handler.Reconcile(ctx, &req)
	if err != nil {
		q.Queue.AddRateLimited(item)
		logger.Error(err, "error while processing queue item")
		return true
	}

	if result.Requeue {
		// It's important that we requeue with rate limiting here, to avoid tightloops for resources
		// that change every time they're reconciled. Note that this diverges from the controller-runtime
		// controller implementation.
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
