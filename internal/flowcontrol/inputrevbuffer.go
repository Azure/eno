package flowcontrol

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// inputRevisionOp is a single pending mutation to one input-revision key of a composition.
// When remove is true the key should be dropped; otherwise revs is written (last-write-wins).
type inputRevisionOp struct {
	revs   *apiv1.InputRevisions
	remove bool
}

// CompositionInputRevisionWriteBuffer reduces load on etcd/apiserver by collecting
// input-revision updates for each Composition over a short window and applying them
// in a single Status().Update per Composition.
//
// Multiple input kinds are watched by independent KindWatchControllers, so a burst of
// input changes affecting the same Composition would otherwise produce one status write
// per change. Coalescing them per Composition (last-write-wins per input key) collapses
// those into a single mutating request while preserving the latest revision for every key.
type CompositionInputRevisionWriteBuffer struct {
	client client.Client

	// queue items are per-composition.
	// the state map collects multiple per-key updates per composition to be dispatched together.
	mut           sync.Mutex
	state         map[types.NamespacedName]map[string]inputRevisionOp
	insertionTime map[types.NamespacedName]time.Time
	// queued tracks whether a composition currently has a queue item awaiting
	// processing. Coupling it with state under mut guarantees that any update
	// enqueued while a flush is in flight re-queues the composition, so an update
	// is never stranded in state without a pending queue item.
	queued map[types.NamespacedName]bool
	queue  workqueue.TypedRateLimitingInterface[types.NamespacedName]
}

func NewCompositionInputRevisionWriteBufferForManager(mgr ctrl.Manager) *CompositionInputRevisionWriteBuffer {
	r := NewCompositionInputRevisionWriteBuffer(mgr.GetClient())
	mgr.Add(r)
	return r
}

func NewCompositionInputRevisionWriteBuffer(cli client.Client) *CompositionInputRevisionWriteBuffer {
	q := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.NewTypedItemExponentialFailureRateLimiter[types.NamespacedName](time.Millisecond*100, 8*time.Second),
		workqueue.TypedRateLimitingQueueConfig[types.NamespacedName]{
			Name: "compositionInputRevisionWriteBuffer",
		})
	return &CompositionInputRevisionWriteBuffer{
		client:        cli,
		state:         make(map[types.NamespacedName]map[string]inputRevisionOp),
		insertionTime: make(map[types.NamespacedName]time.Time),
		queued:        make(map[types.NamespacedName]bool),
		queue:         q,
	}
}

// PatchInputRevisionAsync enqueues an input-revision update for the given composition.
// The update is coalesced last-write-wins per input key and eventually flushed, or dropped
// only if the composition is deleted.
func (w *CompositionInputRevisionWriteBuffer) PatchInputRevisionAsync(comp types.NamespacedName, revs *apiv1.InputRevisions) {
	w.enqueue(comp, revs.Key, inputRevisionOp{revs: revs})
}

// RemoveInputRevisionAsync enqueues the removal of a single input-revision key for the given
// composition. Coalesced last-write-wins with any pending update for the same key.
func (w *CompositionInputRevisionWriteBuffer) RemoveInputRevisionAsync(comp types.NamespacedName, key string) {
	w.enqueue(comp, key, inputRevisionOp{remove: true})
}

func (w *CompositionInputRevisionWriteBuffer) enqueue(comp types.NamespacedName, key string, op inputRevisionOp) {
	w.mut.Lock()
	defer w.mut.Unlock()

	ops := w.state[comp]
	if ops == nil {
		ops = make(map[string]inputRevisionOp)
		w.state[comp] = ops
	}
	ops[key] = op // last write wins

	if _, found := w.insertionTime[comp]; !found {
		w.insertionTime[comp] = time.Now()
	}
	if !w.queued[comp] {
		w.queued[comp] = true
		w.queue.AddRateLimited(comp)
	}
}

func (w *CompositionInputRevisionWriteBuffer) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		w.queue.ShutDown()
	}()
	for w.processQueueItem(ctx) {
	}
	return nil
}

func (w *CompositionInputRevisionWriteBuffer) processQueueItem(ctx context.Context) bool {
	comp, shutdown := w.queue.Get()
	if shutdown {
		return false
	}
	defer w.queue.Done(comp)

	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "controller", "compositionInputRevisionWriteBuffer")
	ctx = logr.NewContext(ctx, logger)

	w.mut.Lock()
	// Mark not-queued up front (under the same lock that guards state) so that any
	// update enqueued from here on re-queues the composition instead of being stranded.
	w.queued[comp] = false
	insertionTime := w.insertionTime[comp]
	ops := w.state[comp]
	delete(w.state, comp)
	w.mut.Unlock()

	if len(ops) == 0 {
		w.queue.Forget(comp)
		w.mut.Lock()
		delete(w.insertionTime, comp)
		w.mut.Unlock()
		return true
	}

	if w.updateComposition(ctx, insertionTime, comp, ops) {
		w.queue.Forget(comp)
		w.mut.Lock()
		delete(w.insertionTime, comp)
		w.mut.Unlock()
		return true
	}

	// Put the updates back in the buffer to retry, without clobbering newer updates
	// that arrived while we were flushing, and ensure the composition is re-queued.
	w.mut.Lock()
	pending := w.state[comp]
	if pending == nil {
		w.state[comp] = ops
	} else {
		for key, op := range ops {
			if _, ok := pending[key]; !ok {
				pending[key] = op
			}
		}
	}
	if !w.queued[comp] {
		w.queued[comp] = true
		w.queue.AddRateLimited(comp)
	}
	w.mut.Unlock()
	return true
}

// updateComposition applies all pending per-key ops to the composition in a single
// Status().Update. Returns true when the work is done (including no-op and composition-deleted),
// false when it should be retried.
func (w *CompositionInputRevisionWriteBuffer) updateComposition(ctx context.Context, insertionTime time.Time, comp types.NamespacedName, ops map[string]inputRevisionOp) (success bool) {
	logger := logr.FromContextOrDiscard(ctx)

	obj := &apiv1.Composition{}
	err := w.client.Get(ctx, comp, obj)
	if k8serrors.IsNotFound(err) {
		logger.V(1).Info("composition deleted - dropping buffered input revision updates")
		return true
	}
	if err != nil {
		logger.Error(err, "unable to get composition")
		return false
	}

	modified := false
	for key, op := range ops {
		if op.remove {
			if removeInputRevision(obj, key) {
				modified = true
			}
			continue
		}
		if setInputRevisions(obj, op.revs) {
			modified = true
		}
	}
	if !modified {
		return true
	}

	err = w.client.Status().Update(ctx, obj)
	if k8serrors.IsNotFound(err) {
		logger.V(1).Info("composition deleted - dropping buffered input revision updates")
		return true
	}
	if k8serrors.IsConflict(err) {
		logger.V(1).Info("conflict updating composition input revisions - will retry")
		return false
	}
	if err != nil {
		logger.Error(err, "unable to update composition input revisions")
		return false
	}

	logger.V(1).Info("flushed input revision updates to composition", "keyCount", len(ops), "latencyMs", time.Since(insertionTime).Abs().Milliseconds())
	return true
}

// setInputRevisions sets or replaces the revision for revs.Key on the composition.
// Returns true when the composition's status was changed.
func setInputRevisions(comp *apiv1.Composition, revs *apiv1.InputRevisions) bool {
	for i, ir := range comp.Status.InputRevisions {
		if ir.Key != revs.Key {
			continue
		}
		if reflect.DeepEqual(ir, *revs) {
			return false
		}
		comp.Status.InputRevisions[i] = *revs
		return true
	}
	comp.Status.InputRevisions = append(comp.Status.InputRevisions, *revs)
	return true
}

// removeInputRevision drops the revision for key from the composition.
// Returns true when the composition's status was changed.
func removeInputRevision(comp *apiv1.Composition, key string) bool {
	for i, ir := range comp.Status.InputRevisions {
		if ir.Key == key {
			comp.Status.InputRevisions = append(comp.Status.InputRevisions[:i], comp.Status.InputRevisions[i+1:]...)
			return true
		}
	}
	return false
}
