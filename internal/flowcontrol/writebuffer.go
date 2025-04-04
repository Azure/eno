package flowcontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
)

type StatusPatchFn func(*apiv1.ResourceState) *apiv1.ResourceState

type resourceSliceStatusUpdate struct {
	SlicedResource *resource.ManifestRef
	PatchFn        StatusPatchFn
}

// ResourceSliceWriteBuffer reduces load on etcd/apiserver by collecting resource slice status
// updates over a short period of time and applying them in a single patch request.
type ResourceSliceWriteBuffer struct {
	client client.Client

	// queue items are per-slice.
	// the state map collects multiple updates per slice to be dispatched by next queue item.
	mut           sync.Mutex
	state         map[types.NamespacedName][]*resourceSliceStatusUpdate
	insertionTime map[types.NamespacedName]time.Time
	queue         workqueue.RateLimitingInterface
}

func NewResourceSliceWriteBufferForManager(mgr ctrl.Manager) *ResourceSliceWriteBuffer {
	r := NewResourceSliceWriteBuffer(mgr.GetClient())
	mgr.Add(r)
	return r
}

func NewResourceSliceWriteBuffer(cli client.Client) *ResourceSliceWriteBuffer {
	return &ResourceSliceWriteBuffer{
		client:        cli,
		state:         make(map[types.NamespacedName][]*resourceSliceStatusUpdate),
		insertionTime: make(map[types.NamespacedName]time.Time),
		queue: workqueue.NewRateLimitingQueueWithConfig(
			workqueue.NewItemExponentialFailureRateLimiter(time.Millisecond*100, 8*time.Second),
			workqueue.RateLimitingQueueConfig{
				Name: "writeBuffer",
			}),
	}
}

// PatchStatusAsync returns after enqueueing the given status update. The update will eventually be applied, or dropped only if the slice is deleted.
func (w *ResourceSliceWriteBuffer) PatchStatusAsync(ctx context.Context, ref *resource.ManifestRef, patchFn StatusPatchFn) {
	w.mut.Lock()
	defer w.mut.Unlock()

	key := ref.Slice
	currentSlice := w.state[key]
	for i, item := range currentSlice {
		if *item.SlicedResource == *ref {
			// last write wins
			currentSlice[i].PatchFn = patchFn
			return
		}
	}

	if _, found := w.insertionTime[key]; !found {
		w.insertionTime[key] = time.Now()
	}
	w.state[key] = append(currentSlice, &resourceSliceStatusUpdate{
		SlicedResource: ref,
		PatchFn:        patchFn,
	})
	if w.queue.NumRequeues(key) == 0 {
		w.queue.AddRateLimited(key)
	}
}

func (w *ResourceSliceWriteBuffer) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		w.queue.ShutDown()
	}()
	for w.processQueueItem(ctx) {
	}
	return nil
}

func (w *ResourceSliceWriteBuffer) processQueueItem(ctx context.Context) bool {
	item, shutdown := w.queue.Get()
	if shutdown {
		return false
	}
	defer w.queue.Done(item)
	sliceNSN := item.(types.NamespacedName)

	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", sliceNSN.Name, "resourceSliceNamespace", sliceNSN.Namespace, "controller", "writeBuffer")
	ctx = logr.NewContext(ctx, logger)

	w.mut.Lock()
	insertionTime := w.insertionTime[sliceNSN]
	updates := w.state[sliceNSN]
	delete(w.state, sliceNSN)

	// Limit the number of operations per patch request
	const max = (10000 / 2) - 2 // 2 ops to initialize status + 2 ops per resource
	if len(updates) > max {
		w.state[sliceNSN] = updates[max:]
		updates = updates[:max]
	}
	w.mut.Unlock()

	// We only forget the rate limit once the update queue for this slice is empty.
	// So the first write is fast, but a steady stream of writes will be throttled exponentially.
	if len(updates) == 0 {
		w.queue.Forget(item)
		w.mut.Lock()
		delete(w.insertionTime, sliceNSN)
		w.mut.Unlock()
		return true // nothing to do
	}

	if w.updateSlice(ctx, insertionTime, sliceNSN, updates) {
		w.queue.AddRateLimited(item)
		return true
	}

	// Put the updates back in the buffer to retry on the next attempt
	w.mut.Lock()
	w.state[sliceNSN] = append(updates, w.state[sliceNSN]...)
	w.mut.Unlock()
	w.queue.AddRateLimited(item)

	return true
}

func (w *ResourceSliceWriteBuffer) updateSlice(ctx context.Context, insertionTime time.Time, sliceNSN types.NamespacedName, updates []*resourceSliceStatusUpdate) (success bool) {
	logger := logr.FromContextOrDiscard(ctx)

	slice := &apiv1.ResourceSlice{}
	slice.Name = sliceNSN.Name
	slice.Namespace = sliceNSN.Namespace
	err := w.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
	if client.IgnoreNotFound(err) != nil {
		logger.Error(err, "unable to get resource slice")
		return false
	}

	patches := w.buildPatch(slice, updates)
	if len(patches) == 0 {
		return true // nothing to do!
	}

	patchJson, err := json.Marshal(&patches)
	if err != nil {
		logger.Error(err, "unable to encode patch")
		return false
	}
	err = w.client.Status().Patch(ctx, slice, client.RawPatch(types.JSONPatchType, patchJson))
	if errors.IsNotFound(err) {
		logger.V(1).Info("resource slice deleted - dropping buffered status updates")
		return true
	}
	if err != nil {
		logger.Error(err, "unable to update resource slice")
		return false
	}

	logger.V(0).Info(fmt.Sprintf("updated the status of %d resources in slice", len(updates)), "latency", time.Since(insertionTime).Abs().Milliseconds())
	sliceStatusUpdates.Inc()
	return true
}

func (*ResourceSliceWriteBuffer) buildPatch(slice *apiv1.ResourceSlice, updates []*resourceSliceStatusUpdate) []*jsonPatch {
	var patches []*jsonPatch
	unsafeSlice := slice.Status.Resources

	if len(unsafeSlice) == 0 {
		patches = append(patches,
			&jsonPatch{
				Op:    "add",
				Path:  "/status",
				Value: map[string]any{},
			},
			&jsonPatch{
				Op:    "test",
				Path:  "/status/resources",
				Value: nil,
			},
			&jsonPatch{
				Op:    "add",
				Path:  "/status/resources",
				Value: make([]apiv1.ResourceState, len(slice.Spec.Resources)),
			})
	}

	for _, update := range updates {
		if update.SlicedResource.Index > len(slice.Spec.Resources)-1 || update.SlicedResource.Index < 0 {
			continue // impossible
		}

		var unsafeStatusPtr *apiv1.ResourceState
		if len(unsafeSlice) <= update.SlicedResource.Index {
			unsafeStatusPtr = &apiv1.ResourceState{}
		} else {
			unsafeStatusPtr = &unsafeSlice[update.SlicedResource.Index]
		}

		patch := update.PatchFn(unsafeStatusPtr)
		if patch == nil {
			continue
		}

		path := fmt.Sprintf("/status/resources/%d", update.SlicedResource.Index)
		patches = append(patches,
			&jsonPatch{
				Op:    "test", // make sure the current state is equal to the state we built the patch against
				Path:  path,
				Value: unsafeStatusPtr,
			},
			&jsonPatch{
				Op:    "replace",
				Path:  path,
				Value: patch,
			})
	}

	return patches
}

type jsonPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}
