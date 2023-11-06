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
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

type asyncStatusUpdate struct {
	SlicedResource *ManifestRef
	PatchFn        StatusPatchFn
}

// writeBuffer reduces load on etcd/apiserver by collecting resource slice status
// updates over a short period of time and applying them in a single update request.
type writeBuffer struct {
	client client.Client
	logger logr.Logger

	// queue items are per-slice.
	// the state map collects multiple updates per slice to be dispatched by next queue item.
	mut   sync.Mutex
	state map[types.NamespacedName][]*asyncStatusUpdate
	queue workqueue.RateLimitingInterface
}

func newWriteBuffer(cli client.Client, logger logr.Logger, batchInterval time.Duration, burst int) *writeBuffer {
	return &writeBuffer{
		client: cli,
		logger: logger.WithValues("controller", "writeBuffer"),
		state:  make(map[types.NamespacedName][]*asyncStatusUpdate),
		queue: workqueue.NewRateLimitingQueueWithConfig(
			&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Every(batchInterval), burst)},
			workqueue.RateLimitingQueueConfig{
				Name: "writeBuffer",
			}),
	}
}

func (w *writeBuffer) PatchStatusAsync(ctx context.Context, ref *ManifestRef, patchFn StatusPatchFn) {
	w.mut.Lock()
	defer w.mut.Unlock()

	logr.FromContextOrDiscard(ctx).V(1).Info("buffering status update")

	key := ref.Slice
	// TODO(jordan): Consider de-duping this slice to avoid potentially allocating a lot of memory if some bug causes churning of the control loop that ends up calling this.
	w.state[key] = append(w.state[key], &asyncStatusUpdate{
		SlicedResource: ref,
		PatchFn:        patchFn,
	})
	w.queue.AddRateLimited(key)
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

	logger := w.logger.WithValues("slice", sliceNSN)
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
	w.queue.Forget(item)
	w.queue.AddRateLimited(item)

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
		slice.Status.Resources = make([]apiv1.ResourceState, len(slice.Spec.Resources))
	}

	var dirty bool
	for _, update := range updates {
		logger := logger.WithValues("slicedResource", update.SlicedResource)
		statusPtr := &slice.Status.Resources[update.SlicedResource.Index]

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
