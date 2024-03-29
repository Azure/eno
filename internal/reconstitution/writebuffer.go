package reconstitution

import (
	"context"
	"encoding/json"
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

func (w *writeBuffer) PatchStatusAsync(ctx context.Context, ref *ManifestRef, patchFn StatusPatchFn) {
	w.mut.Lock()
	defer w.mut.Unlock()

	key := ref.Slice
	currentSlice := w.state[key]
	for i, item := range currentSlice {
		if *item.SlicedResource == *ref {
			currentSlice[i].PatchFn = patchFn
			return
		}
	}

	w.state[key] = append(currentSlice, &asyncStatusUpdate{
		SlicedResource: ref,
		PatchFn:        patchFn,
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
	w.mut.Lock()
	w.state[sliceNSN] = append(updates, w.state[sliceNSN]...)
	w.mut.Unlock()
	w.queue.AddRateLimited(item)

	return true
}

func (w *writeBuffer) updateSlice(ctx context.Context, sliceNSN types.NamespacedName, updates []*asyncStatusUpdate) bool {
	logger := logr.FromContextOrDiscard(ctx)

	slice := &apiv1.ResourceSlice{}
	slice.Name = sliceNSN.Name
	slice.Namespace = sliceNSN.Namespace
	err := w.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
	if client.IgnoreNotFound(err) != nil {
		logger.Error(err, "unable to get resource slice")
		return false
	}

	// Sending an empty resource version in update requests never returns 404 or 409.
	// Instead, input validation will fail for every request regardless of the resource's actual state.
	// So we need to set an incorrect but valid resource version in order for the 404 checks below to work.
	//
	// This is necessary because we can't trust that the informer's 404 means the resource is actually deleted - its cache may just be stale.
	// So we defer the 404 check to the update.
	if errors.IsNotFound(err) {
		slice.ResourceVersion = "1"
	}

	// It's easier to pre-allocate the entire status slice before sending patches
	// since the "replace" op requires an existing item.
	if len(slice.Status.Resources) == 0 {
		copy := slice.DeepCopy()
		copy.Status.Resources = make([]apiv1.ResourceState, len(slice.Spec.Resources))
		err = w.client.Status().Update(ctx, copy)
		if errors.IsNotFound(err) {
			logger.V(1).Info("resource slice has been deleted - dropping enqueued status update")
			return true
		}
		if err != nil {
			logger.Error(err, "unable to update resource slice")
			return false
		}
		slice = copy
	}

	// Transform the set of patch funcs into a set of jsonpatch objects
	unsafeSlice := slice.Status.Resources
	var patches []*jsonPatch
	for _, update := range updates {
		unsafeStatusPtr := &unsafeSlice[update.SlicedResource.Index]
		patch := update.PatchFn(unsafeStatusPtr)
		if patch == nil {
			continue
		}

		patches = append(patches, &jsonPatch{
			Op:    "replace",
			Path:  fmt.Sprintf("/status/resources/%d", update.SlicedResource.Index),
			Value: patch,
		})
	}
	if len(patches) == 0 {
		return true // nothing to do!
	}

	// Encode/apply the patch(es)
	patchJson, err := json.Marshal(&patches)
	if err != nil {
		logger.Error(err, "unable to encode patch")
		return false
	}
	err = w.client.Status().Patch(ctx, slice, client.RawPatch(types.JSONPatchType, patchJson))
	if err != nil {
		logger.Error(err, "unable to update resource slice")
		return false
	}

	logger.V(0).Info(fmt.Sprintf("updated the status of %d resources in slice", len(updates)))
	sliceStatusUpdates.Inc()
	return true
}

type jsonPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}
