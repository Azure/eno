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
	CheckFn        CheckPatchFn
}

// writeBuffer reduces load on etcd/apiserver by collecting resource slice status
// updates over a short period of time and applying them in a single update request.
type writeBuffer struct {
	client client.Client

	// queue items are per-slice.
	// the state map collects multiple updates per slice to be dispatched by next queue item.
	mut   sync.Mutex
	state map[types.NamespacedName][]*asyncStatusUpdate
	queue workqueue.RateLimitingInterface
}

func newWriteBuffer(cli client.Client, batchInterval time.Duration, burst int) *writeBuffer {
	return &writeBuffer{
		client: cli,
		state:  make(map[types.NamespacedName][]*asyncStatusUpdate),
		queue: workqueue.NewRateLimitingQueueWithConfig(
			&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Every(batchInterval), burst)},
			workqueue.RateLimitingQueueConfig{
				Name: "writeBuffer",
			}),
	}
}

func (w *writeBuffer) PatchStatusAsync(ctx context.Context, ref *ManifestRef, checkFn CheckPatchFn, patchFn StatusPatchFn) {
	w.mut.Lock()
	defer w.mut.Unlock()
	logger := logr.FromContextOrDiscard(ctx)

	key := ref.Slice
	currentSlice := w.state[key]
	for _, item := range currentSlice {
		if *item.SlicedResource == *ref {
			logger.V(2).Info("dropping async resource status update because another change is already buffered for this resource")
			return
		}
	}

	w.state[key] = append(currentSlice, &asyncStatusUpdate{
		SlicedResource: ref,
		PatchFn:        patchFn,
		CheckFn:        checkFn,
	})
	w.queue.Add(key)
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

	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", sliceNSN.Name, "resourceSliceNamespace", sliceNSN.Namespace, "controller", "writeBuffer")
	ctx = logr.NewContext(ctx, logger)

	w.mut.Lock()
	updates := w.state[sliceNSN]
	delete(w.state, sliceNSN)
	w.mut.Unlock()

	if len(updates) == 0 {
		w.queue.Forget(item)
		return true // nothing to do
	}

	if w.updateSlice(ctx, sliceNSN, updates) {
		w.queue.Forget(item)
		w.queue.AddRateLimited(item)
		return true
	}

	// Put the updates back in the buffer to retry on the next attempt
	logger.V(1).Info("update failed - adding updates back to the buffer")
	w.mut.Lock()
	w.state[sliceNSN] = append(updates, w.state[sliceNSN]...)
	w.mut.Unlock()
	w.queue.AddRateLimited(item)

	return true
}

func (w *writeBuffer) updateSlice(ctx context.Context, sliceNSN types.NamespacedName, updates []*asyncStatusUpdate) bool {
	logger := logr.FromContextOrDiscard(ctx)

	slice := &apiv1.ResourceSlice{}
	err := w.client.Get(ctx, sliceNSN, slice)
	if errors.IsNotFound(err) {
		// TODO: I think this should cause the work queue to Forget this item?
		logger.V(0).Info("slice has been deleted, skipping status update")
		return true
	}
	if err != nil {
		logger.Error(err, "unable to get resource slice")
		return false
	}

	// Allocate the status slice if needed
	var status *apiv1.ResourceSliceStatus
	var dirty bool
	if len(slice.Status.Resources) != len(slice.Spec.Resources) {
		dirty = true
		status = slice.Status.DeepCopy()
		status.Resources = make([]apiv1.ResourceState, len(slice.Spec.Resources))
	}

	// Use the newly allocated slice (if allocated), otherwise use the already allocated one
	unsafeSlice := slice.Status.Resources
	if unsafeSlice == nil {
		unsafeSlice = status.Resources
	}

	// The resource slice informer doesn't DeepCopy automatically for perf reasons.
	// So we can apply the CheckFn to status pointers but not PatchFn.
	// We'll deep copy the status here only when it needs to change.
	for _, update := range updates {
		unsafeStatusPtr := &unsafeSlice[update.SlicedResource.Index]
		if update.CheckFn(unsafeStatusPtr) {
			continue
		}

		if status == nil {
			status = slice.Status.DeepCopy()
		}

		dirty = true
		update.PatchFn(&status.Resources[update.SlicedResource.Index])
	}
	if !dirty {
		return true
	}

	copy := &apiv1.ResourceSlice{Status: *status}
	slice.ObjectMeta.DeepCopyInto(&copy.ObjectMeta)
	err = w.client.Status().Update(ctx, copy)
	if err != nil {
		logger.Error(err, "unable to update resource slice")
		return false
	}

	logger.V(0).Info(fmt.Sprintf("updated the status of %d resources in slice", len(updates)))
	return true
}
