package reconstitution

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestQueueProcessorRequeueLogic(t *testing.T) {
	rateLimiter := workqueue.DefaultItemBasedRateLimiter()
	queue := workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lastCall := time.Now()
	reconciler := reconcilerFunc(func(ctx context.Context, req *Request) (ctrl.Result, error) {
		delta := time.Since(lastCall)
		t.Logf("delta: %s", delta)
		if delta > time.Millisecond*30 {
			cancel() // break the test loop
		}
		return ctrl.Result{Requeue: true}, nil
	})
	q := &queueProcessor{
		Queue:   queue,
		Handler: reconciler,
		Logger:  testr.New(t),
	}
	q.Queue.Add(&Request{}) // single queue item
	q.Start(ctx)
}

type reconcilerFunc func(ctx context.Context, req *Request) (ctrl.Result, error)

func (r reconcilerFunc) Reconcile(ctx context.Context, req *Request) (ctrl.Result, error) {
	return r(ctx, req)
}
