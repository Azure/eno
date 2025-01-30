package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// controller schedules compositions for synthesis based on a global view of all compositions and synthesizers.
//
// - Initializes and manages synthesis state (uuid, initialized timestamp, etc.)
// - Rolls out synthesizer and deferred input changes while honoring a cluster-wide cooldown period
// - Enforces a global synthesis concurrency limit
// - Prioritizes operations
type controller struct {
	client           client.Client
	concurrencyLimit int
	cooldownPeriod   time.Duration
	cacheGracePeriod time.Duration

	lastApplied *op
}

func NewController(mgr ctrl.Manager, concurrencyLimit int, cooldown time.Duration) error {
	c := &controller{
		client:           mgr.GetClient(),
		concurrencyLimit: concurrencyLimit,
		cooldownPeriod:   cooldown,
		cacheGracePeriod: time.Second,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("schedulingController").
		Watches(&apiv1.Composition{}, manager.SingleEventHandler()).
		Watches(&apiv1.Synthesizer{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "schedulingController")).
		Complete(c)
}

func (c *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	start := time.Now()
	defer func() {
		schedulingLatency.Observe(time.Since(start).Seconds())
	}()

	if c.lastApplied != nil {
		ok, wait, err := c.lastApplied.HasBeenPatched(ctx, c.client, c.cacheGracePeriod)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("checking cache for previous op: %w", err)
		}
		if !ok {
			logger.V(1).Info("waiting for cache to reflect previous operation")
			return ctrl.Result{RequeueAfter: wait}, nil
		}
		c.lastApplied = nil
	}

	synths := &apiv1.SynthesizerList{}
	err := c.client.List(ctx, synths)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing synthesizers: %w", err)
	}
	synthsByName := map[string]apiv1.Synthesizer{}
	for _, synth := range synths.Items {
		synthsByName[synth.Name] = synth
	}

	comps := &apiv1.CompositionList{}
	err = c.client.List(ctx, comps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}
	nextSlot := c.getNextCooldownSlot(comps)

	var inFlight int
	var op *op
	for _, comp := range comps.Items {
		comp := comp
		if comp.Synthesizing() {
			inFlight++
		}

		synth, ok := synthsByName[comp.Spec.Synthesizer.Name]
		if !ok {
			continue
		}

		next := newOp(&synth, &comp)
		if next != nil && (op == nil || op.Less(next)) {
			op = next
		}
	}
	freeSynthesisSlots.Set(float64(c.concurrencyLimit - inFlight))

	if op == nil || inFlight >= c.concurrencyLimit {
		return ctrl.Result{}, nil
	}

	if wait := time.Until(nextSlot); op.Reason.Deferred() && wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	logger = logger.WithValues("compositionName", op.Composition.Name, "compositionNamespace", op.Composition.Namespace, "reason", op.Reason)

	if err := c.dispatchOp(ctx, op); err != nil {
		if errors.IsInvalid(err) {
			logger.V(0).Info("conflict while dispatching synthesis")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("dispatching synthesis operation: %w", err)
	}

	op.Dispatched = time.Now()
	c.lastApplied = op
	logger.V(0).Info("dispatched synthesis")

	return ctrl.Result{}, nil
}

// getNextCooldownSlot returns the next time at which a deferred synthesis can be dispatched while honoring the configured cooldown period.
func (c *controller) getNextCooldownSlot(comps *apiv1.CompositionList) time.Time {
	var last time.Time
	for _, comp := range comps.Items {
		syn := comp.Status.CurrentSynthesis
		if syn != nil && syn.Deferred && syn.Initialized != nil && syn.Initialized.Time.After(last) {
			last = syn.Initialized.Time
		}
	}
	return last.Add(c.cooldownPeriod)
}

func (c *controller) dispatchOp(ctx context.Context, op *op) error {
	patch, err := json.Marshal(op.BuildPatch())
	if err != nil {
		return err
	}
	return c.client.Status().Patch(ctx, op.Composition.DeepCopy(), client.RawPatch(types.JSONPatchType, patch))
}
