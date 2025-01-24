package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// TODO: Rework pod timeouts / retries to re-enter the scheduler

var debug = os.Getenv("ENO_SCHEDULING_DEBUG") == "true"

type controller struct {
	client           client.Client
	concurrencyLimit int
}

func NewController(mgr ctrl.Manager, concurrencyLimit int) error {
	c := &controller{
		client:           mgr.GetClient(),
		concurrencyLimit: concurrencyLimit,
	}
	// TODO: Event filter
	return ctrl.NewControllerManagedBy(mgr).
		Named("schedulingController").
		Watches(&apiv1.Composition{}, manager.SingleEventHandler()).
		Watches(&apiv1.Synthesizer{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "schedulingController")).
		Complete(c)
}

func (c *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comps := &apiv1.CompositionList{}
	err := c.client.List(ctx, comps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	queue, inFlight := c.buildOps(ctx, comps)
	if debug {
		logger.V(1).Info("scheduling queue state", "queue", fmt.Sprintf("%+s", queue))
	}
	inFlight = c.dispatchOps(ctx, queue, inFlight)

	// TODO: metrics
	// - Active operation count
	// - Queue length (beyond the concurrency limit)

	// TODO: How can we block the loop until the last write we made hits the informer?
	// - Can we guarantee that every write will hit this controller, and use a counter?
	// - It might be tricky to juggle the finalizer

	return ctrl.Result{
		Requeue: len(queue) > 0 && inFlight < c.concurrencyLimit,
	}, nil
}

func (c *controller) buildOps(ctx context.Context, comps *apiv1.CompositionList) ([]*op, int) {
	logger := logr.FromContextOrDiscard(ctx)

	var inFlight int
	var queue []*op
	for _, comp := range comps.Items {
		comp := comp

		if comp.Spec.Synthesizer.Name == "" {
			continue
		}

		if comp.Synthesizing() {
			inFlight++
		}

		synth := &apiv1.Synthesizer{}
		err := c.client.Get(ctx, client.ObjectKey{Name: comp.Spec.Synthesizer.Name, Namespace: comp.Namespace}, synth)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			logger.Error(err, "unable to get synthesizer for composition", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesizerName", comp.Spec.Synthesizer.Name)
			continue
		}

		op := c.buildOp(synth, &comp)
		if op != nil {
			queue = append(queue, op)
		}
	}

	prioritizeOps(queue)
	return queue, inFlight
}

func (c *controller) buildOp(synth *apiv1.Synthesizer, comp *apiv1.Composition) *op {
	if (!comp.InputsExist(synth) || comp.InputsOutOfLockstep(synth)) && comp.DeletionTimestamp == nil {
		return nil // wait for inputs
	}

	syn := comp.Status.CurrentSynthesis
	o := &op{Composition: comp}
	if syn == nil {
		o.Reason = "InitialSynthesis"
		return o
	}

	if syn.ObservedCompositionGeneration != comp.Generation {
		o.Reason = "CompositionModified"
		return o
	}

	if !inputRevisionsEqual(synth, comp.Status.InputRevisions, syn.InputRevisions) && syn.Synthesized != nil && !comp.ShouldIgnoreSideEffects() {
		o.Reason = "InputModified"
		return o
	}

	// TODO: Check the deferred inputs, synthesizer generation, and maybe annotation for self healing

	return nil
}

func (c *controller) dispatchOps(ctx context.Context, queue []*op, inFlight int) int {
	logger := logr.FromContextOrDiscard(ctx)

	for _, op := range queue {
		if inFlight >= c.concurrencyLimit {
			return inFlight
		}

		if err := c.dispatchOp(ctx, op); err != nil {
			logger.Error(err, "unable to dispatch synthesis", "compositionName", op.Composition.Name, "compositionNamespace", op.Composition.Namespace)
			continue // this is safe - one bad op shouldn't block the entire loop
		}
		logger.V(0).Info("dispatched synthesis", "compositionName", op.Composition.Name, "compositionNamespace", op.Composition.Namespace)

		if !op.Composition.Synthesizing() {
			inFlight++
		}
	}
	return inFlight
}

func (c *controller) dispatchOp(ctx context.Context, op *op) error {
	patch, err := json.Marshal(op.Patch())
	if err != nil {
		return err
	}
	return c.client.Status().Patch(ctx, op.Composition, client.RawPatch(types.JSONPatchType, patch))
}

func prioritizeOps(queue []*op) {
	sort.Slice(queue, func(i, j int) bool { return queue[i].LowerPriority(queue[j]) })
}

// inputRevisionsEqual compares two sets of input revisions while ignoring deferred values.
// TODO: Rename
func inputRevisionsEqual(synth *apiv1.Synthesizer, a, b []apiv1.InputRevisions) bool {
	if len(a) != len(b) {
		return false
	}

	refsByKey := map[string]apiv1.Ref{}
	for _, ref := range synth.Spec.Refs {
		ref := ref
		refsByKey[ref.Key] = ref
	}

	// It's important that ordering isn't strict since input revisions may
	// either be ordered by the corresponding refs, or appended to the slice
	// as they are discovered by the watch controller
	sort.Slice(a, func(i, j int) bool { return a[i].Key < a[j].Key })
	sort.Slice(b, func(i, j int) bool { return b[i].Key < b[j].Key })

	var equal int
	for i, ar := range a {
		br := b[i]
		if ref, exists := refsByKey[ar.Key]; exists && ref.Defer {
			equal++
			continue // ignore deferred inputs
		}

		if ar.Equal(br) {
			equal++
		}
	}

	return equal == len(a)
}

type op struct {
	Composition *apiv1.Composition
	Reason      string
}

func (o *op) LowerPriority(other *op) bool {
	// TODO: Use some random jitter to ensure that pending rollouts eventually happen?
	return false
}

func (o *op) String() string {
	return fmt.Sprintf("op{composition=%s/%s, reason=%s}", o.Composition.Namespace, o.Composition.Name, o.Reason)
}

func (o *op) Patch() any {
	ops := []map[string]any{}

	if o.Composition.Status.Zero() {
		ops = append(ops,
			map[string]any{
				"op":    "test",
				"path":  "/status",
				"value": nil,
			},
			map[string]any{
				"op":    "add",
				"path":  "/status",
				"value": map[string]any{},
			})
	}

	if syn := o.Composition.Status.CurrentSynthesis; syn != nil {
		ops = append(ops, map[string]any{
			"op":    "test",
			"path":  "/status/currentSynthesis/uuid",
			"value": syn.UUID,
		})

		if syn.Synthesized != nil && !syn.Failed() {
			ops = append(ops, map[string]any{
				"op":    "replace",
				"path":  "/status/previousSynthesis",
				"value": syn,
			})
		}
	}

	ops = append(ops, map[string]any{
		"op":   "replace",
		"path": "/status/currentSynthesis",
		"value": map[string]any{
			"observedCompositionGeneration": o.Composition.Generation,
			"initialized":                   time.Now().Format(time.RFC3339),
			"uuid":                          uuid.NewString(),
		},
	})

	return ops
}
