package reconstitution

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

type asyncStatusUpdate struct {
	SlicedResource *SlicedResourceRef
	PatchFn        func(*apiv1.ResourceStatus) bool
}

type writeBuffer struct {
	*reconstituter
	client client.Client
	logger logr.Logger

	mut   sync.Mutex
	state map[types.NamespacedName][]*asyncStatusUpdate
	queue workqueue.RateLimitingInterface
}

func newWriteBuffer(mgr ctrl.Manager, recon *reconstituter, writeBatchInterval time.Duration) *writeBuffer {
	w := &writeBuffer{
		reconstituter: recon,
		client:        mgr.GetClient(),
		logger:        mgr.GetLogger().WithValues("controller", "writeBuffer"),
		state:         make(map[types.NamespacedName][]*asyncStatusUpdate),
		queue: workqueue.NewRateLimitingQueueWithConfig(
			&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Every(writeBatchInterval), 2)},
			workqueue.RateLimitingQueueConfig{
				Name: "writeBuffer",
			}),
	}
	mgr.Add(w)
	return w
}

func (w *writeBuffer) PatchStatusAsync(ctx context.Context, req *Request, patchFn func(*apiv1.ResourceStatus) bool) {
	w.mut.Lock()
	defer w.mut.Unlock()

	w.Logger.V(1).WithValues(req.LogValues()...).Info("buffering status update")

	key := req.SlicedResource.SliceResource
	w.state[key] = append(w.state[key], &asyncStatusUpdate{
		SlicedResource: &req.SlicedResource,
		PatchFn:        patchFn,
	})
}

func (w *writeBuffer) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		w.queue.ShutDown()
	}()
	for w.processQueueItem(ctx) {
	}
	return nil
}

func (w *writeBuffer) processQueueItem(ctx context.Context) bool {
	item, shutdown := w.queue.Get()
	if shutdown {
		return false
	}
	defer w.queue.Done(item)
	sliceNSN := item.(types.NamespacedName)

	w.mut.Lock()
	updates := w.state[sliceNSN]
	delete(w.state, sliceNSN)
	w.mut.Unlock()

	logger := w.Logger.WithValues("slice", sliceNSN)
	ctx = logr.NewContext(ctx, logger)

	if len(updates) == 0 {
		logger.V(0).Info("dropping queue item because no updates were found for this slice (this is suspicious)")
		w.queue.Forget(item)
		return true
	}

	if w.updateSlice(ctx, sliceNSN, updates) {
		w.queue.Forget(item)
		return true
	}

	// Put the updates back in the buffer to retry on the next attempt
	logger.V(1).Info("update failed - adding updates back to the buffer")
	w.mut.Lock()
	w.state[sliceNSN] = append(w.state[sliceNSN], updates...)
	w.mut.Unlock()

	return true
}

func (w *writeBuffer) updateSlice(ctx context.Context, sliceNSN types.NamespacedName, updates []*asyncStatusUpdate) bool {
	logger := logr.FromContextOrDiscard(ctx)
	logger.V(1).Info("starting to update slice status")

	slice := &apiv1.ResourceSlice{}
	err := w.client.Get(ctx, sliceNSN, slice)
	if errors.IsNotFound(err) {
		logger.V(0).Info("slice has been deleted, skipping status update")
		return true
	}
	if err != nil {
		logger.Error(err, "unable to get resource slice")
		return false
	}

	if len(slice.Status.Resources) != len(slice.Spec.Resources) {
		logger.V(1).Info("allocating resource status slice")
		slice.Status.Resources = make([]apiv1.ResourceStatus, len(slice.Spec.Resources))
	}

	var dirty bool
	for _, update := range updates {
		logger := logger.WithValues("slicedResource", update.SlicedResource)
		statusPtr := &slice.Status.Resources[update.SlicedResource.ResourceIndex]

		if update.PatchFn(statusPtr) {
			logger.V(1).Info("patch caused status to change")
			dirty = true
		} else {
			logger.V(1).Info("patch did not cause status to change")
		}
	}
	if !dirty {
		logger.V(1).Info("no status updates were necessary")
		return true
	}

	err = w.client.Status().Update(ctx, slice)
	if err != nil {
		logger.Error(err, "unable to update resource slice")
		return false
	}

	logger.V(0).Info(fmt.Sprintf("updated the status of %d resources in slice", len(updates)))
	return true
}
