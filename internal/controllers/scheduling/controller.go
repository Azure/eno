package scheduling

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

const (
	synthEpochAnnotationKey = "eno.azure.io/global-synthesizer-epoch"
)

// controller is responsible for carefully dispatching synthesis operations.
//
// Dispatching synthesis consists of swapping any existing synthesis state to the previous slot,
// which signals to other controllers that a new synthesis is needed. This controller will swap
// the states when the resulting synthesis operation will not cause the cluster-wide concurrency
// limit to be exceeded.
//
// Synthesis is dispatched when the composition spec is modified or when the inputs or synthesizer
// have changed. Deferred inputs and synthesizer changes are subject to a cluster-wide "cooldown period"
// to hedge against bad changes.
//
// The implementation is completely deterministic i.e. given a set of compositions and synthesizers,
// it will always produce the same synthesis order, even if two controllers think they are the current
// leader AND one of them has a newer composition or synthesizer in its informer cache.
//
// Rollout order for synthesizer changes is unique to the generation of the synthesizer.
// Compositions will not receive the new synthesizer in the same order for every generation, but
// the same generation will always roll out in the same order.
type controller struct {
	client            client.Client
	concurrencyLimit  int
	cooldownPeriod    time.Duration
	cacheGracePeriod  time.Duration
	watchdogThreshold time.Duration

	lastApplied *op
}

func NewController(mgr ctrl.Manager, concurrencyLimit int, cooldown, watchdogThreshold time.Duration) error {
	c := &controller{
		client:            mgr.GetClient(),
		concurrencyLimit:  concurrencyLimit,
		cooldownPeriod:    cooldown,
		cacheGracePeriod:  time.Second,
		watchdogThreshold: watchdogThreshold,
	}

	// Non-leaders should report that all slots are available, not zero.
	freeSynthesisSlots.Set(float64(concurrencyLimit))

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

	// Avoid conflict errors by waiting until we see the last dispatched synthesis (or timeout)
	if c.lastApplied != nil {
		ok, wait, err := c.lastApplied.HasBeenPatched(ctx, c.client, c.cacheGracePeriod)
		if err != nil {
			logger.Error(err, "checking cache for previous op")
			return ctrl.Result{}, err
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
		logger.Error(err, "failed to list synthesizers")
		return ctrl.Result{}, err
	}
	synthsByName, synthEpoch := indexSynthesizers(synths.Items)

	comps := &apiv1.CompositionList{}
	err = c.client.List(ctx, comps)
	if err != nil {
		logger.Error(err, "failed to list compositions")
		return ctrl.Result{}, err
	}
	nextSlot := c.getNextCooldownSlot(comps)

	var inFlight int
	var op *op
	for _, comp := range comps.Items {
		comp := comp
		if comp.Synthesizing() {
			inFlight++
		}

		if missedReconciliation(&comp, c.watchdogThreshold) {
			stuckReconciling.WithLabelValues(comp.Spec.Synthesizer.Name).Inc()
		}

		synth, ok := synthsByName[comp.Spec.Synthesizer.Name]
		if !ok {
			continue
		}

		next := newOp(&synth, &comp, nextSlot)
		if next != nil && (op == nil || next.Less(op)) {
			op = next
		}
	}
	freeSynthesisSlots.Set(float64(c.concurrencyLimit - inFlight))

	if op == nil || inFlight >= c.concurrencyLimit {
		return ctrl.Result{}, nil
	}
	if !op.NotBefore.IsZero() { // the next op isn't ready to be dispathced yet
		if wait := time.Until(op.NotBefore); wait > 0 {
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}
	logger = logger.WithValues("compositionName", op.Composition.Name, "compositionNamespace", op.Composition.Namespace, "reason", op.Reason, "synthEpoch", synthEpoch)

	// Maintain ordering across synth/composition informers by doing a 2PC on the composition
	if op.Reason == synthesizerModifiedOp && setSynthEpochAnnotation(op.Composition, synthEpoch) {
		if err := c.client.Update(ctx, op.Composition); err != nil {
			logger.Error(err, "updating synthesizer epoch")
			return ctrl.Result{}, err
		}
		logger.V(1).Info("updated global synthesizer epoch")
		return ctrl.Result{}, nil
	}

	if err := c.dispatchOp(ctx, op); err != nil {
		if errors.IsInvalid(err) {
			logger.Error(err, "conflict while dispatching synthesis")
			return ctrl.Result{}, err
		}
		logger.Error(err, "dispatching synthesis operation")
		return ctrl.Result{}, err
	}

	op.Dispatched = time.Now()
	c.lastApplied = op
	logger.V(0).Info("dispatched synthesis", "synthesisUUID", op.id)

	return ctrl.Result{}, nil
}

// getNextCooldownSlot returns the next time at which a deferred synthesis can be dispatched while honoring the configured cooldown period.
func (c *controller) getNextCooldownSlot(comps *apiv1.CompositionList) time.Time {
	var next time.Time
	for _, comp := range comps.Items {
		for _, syn := range []*apiv1.Synthesis{comp.Status.InFlightSynthesis, comp.Status.CurrentSynthesis, comp.Status.PreviousSynthesis} {
			if syn != nil && syn.Deferred && syn.Initialized != nil && syn.Initialized.Time.After(next) {
				next = syn.Initialized.Time
			}
		}
	}
	return next.Add(c.cooldownPeriod)
}

func (c *controller) dispatchOp(ctx context.Context, op *op) error {
	patch, err := json.Marshal(op.BuildPatch())
	if err != nil {
		return err
	}
	return c.client.Status().Patch(ctx, op.Composition.DeepCopy(), client.RawPatch(types.JSONPatchType, patch))
}

// indexSynthesizers returns an indexed representation of the synthesizers and has the side effect of
// resetting the stuckReconciling metric.
func indexSynthesizers(synths []apiv1.Synthesizer) (byName map[string]apiv1.Synthesizer, epoch string) {
	sort.Slice(synths, func(i, j int) bool { return synths[i].Name < synths[j].Name })
	byName = map[string]apiv1.Synthesizer{}
	h := fnv.New64()
	stuckReconciling.Reset()
	for _, synth := range synths {
		byName[synth.Name] = synth
		fmt.Fprintf(h, "%s:%d", synth.UID, synth.Generation)
		stuckReconciling.WithLabelValues(synth.Name).Set(0)
	}
	return byName, hex.EncodeToString(h.Sum(nil))
}

func setSynthEpochAnnotation(comp *apiv1.Composition, value string) bool {
	anno := comp.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}

	ok := anno[synthEpochAnnotationKey] != value
	anno[synthEpochAnnotationKey] = value
	comp.SetAnnotations(anno)
	return ok
}
