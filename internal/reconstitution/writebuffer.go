package reconstitution

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

type asyncStatusUpdate struct {
	SlicedResource *SlicedResourceRef
	PatchFn        func(*apiv1.ResourceStatus) bool
}

type writeBuffer struct {
	*reconstituter
	Client client.Client

	mut   sync.Mutex
	state map[types.NamespacedName][]*asyncStatusUpdate
	queue workqueue.RateLimitingInterface
}

func newWriteBuffer(mgr ctrl.Manager, recon *reconstituter, writeBatchInterval time.Duration) *writeBuffer {
	w := &writeBuffer{
		reconstituter: recon,
		Client:        mgr.GetClient(),
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
	w.state[sliceNSN] = []*asyncStatusUpdate{}
	w.mut.Unlock()

	if len(updates) == 0 {
		w.queue.Forget(item)
		return true
	}

	if w.updateSlice(ctx, sliceNSN, updates) {
		w.queue.Forget(item)
		return true
	}

	// Put the updates back in the buffer to retry on the next attempt
	w.mut.Lock()
	w.state[sliceNSN] = append(w.state[sliceNSN], updates...)
	w.mut.Unlock()

	return true
}

func (w *writeBuffer) updateSlice(ctx context.Context, sliceNSN types.NamespacedName, updates []*asyncStatusUpdate) bool {
	slice := &apiv1.ResourceSlice{}
	err := w.Client.Get(ctx, sliceNSN, slice)
	if errors.IsNotFound(err) {
		return true
	}
	if err != nil {
		// TODO
		return false
	}

	if len(slice.Status.Resources) != len(slice.Spec.Resources) {
		slice.Status.Resources = make([]apiv1.ResourceStatus, len(slice.Spec.Resources))
	}

	var dirty bool
	for _, update := range updates {
		statusPtr := &slice.Status.Resources[update.SlicedResource.ResourceIndex]
		if update.PatchFn(statusPtr) {
			dirty = true
		}
	}
	if !dirty {
		return true
	}

	err = w.Client.Status().Update(ctx, slice)
	if err != nil {
		// TODO
		return false
	}

	return true
}
