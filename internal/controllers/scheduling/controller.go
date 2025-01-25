package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// TODO: Where to add/remove composition finalizer? Use more than one? Make sure we check finalizer here if needed

var debug = os.Getenv("ENO_SCHEDULING_DEBUG") == "true"

type controller struct {
	client           client.Client
	concurrencyLimit int
	cooldownPeriod   time.Duration
}

func NewController(mgr ctrl.Manager, concurrencyLimit int) error {
	return NewControllerWithCooldown(mgr, concurrencyLimit, time.Second*2)
}

func NewControllerWithCooldown(mgr ctrl.Manager, concurrencyLimit int, cooldown time.Duration) error {
	c := &controller{
		client:           mgr.GetClient(),
		concurrencyLimit: concurrencyLimit,
		cooldownPeriod:   cooldown,
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
	more, deferredUntil := c.dispatchOps(ctx, queue, inFlight)

	// TODO: metrics
	// - Active operation count
	// - Queue length (beyond the concurrency limit)

	// TODO: How can we block the loop until the last write we made hits the informer?
	// - Can we guarantee that every write will hit this controller, and use a counter?
	// - It might be tricky to juggle the finalizer

	res := ctrl.Result{Requeue: more}
	if deferredUntil != nil {
		res.RequeueAfter = time.Until(*deferredUntil)
	}
	return res, nil
}

func (c *controller) buildOps(ctx context.Context, comps *apiv1.CompositionList) ([]*op, int) {
	logger := logr.FromContextOrDiscard(ctx)

	var lastDeferral time.Time
	for _, comp := range comps.Items {
		syn := comp.Status.CurrentSynthesis
		if syn != nil && syn.Deferred && syn.Initialized != nil && syn.Initialized.Time.After(lastDeferral) {
			lastDeferral = syn.Initialized.Time
		}
	}

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

		nextSafeDeferral := lastDeferral.Add(c.cooldownPeriod)
		if now := time.Now(); nextSafeDeferral.Before(now) {
			nextSafeDeferral = now
		}

		op := newOp(synth, &comp, nextSafeDeferral)
		if op != nil {
			queue = append(queue, op)
		}
	}

	prioritizeOps(queue)
	return queue, inFlight
}

func (c *controller) dispatchOps(ctx context.Context, queue []*op, inFlight int) (bool, *time.Time) {
	logger := logr.FromContextOrDiscard(ctx)

	var nextDeferredOp *time.Time
	var deferred int
	for _, op := range queue {
		if inFlight >= c.concurrencyLimit {
			return false, nil
		}

		if op.Deferred() {
			deferred++
			if nextDeferredOp == nil || op.OnlyAfter.Before(*nextDeferredOp) {
				nextDeferredOp = op.OnlyAfter
			}
			continue
		}

		if err := c.dispatchOp(ctx, op); err != nil {
			logger.Error(err, "unable to dispatch synthesis", "compositionName", op.Composition.Name, "compositionNamespace", op.Composition.Namespace)
			continue // this is safe - one bad op shouldn't block the entire loop
		}
		logger.V(0).Info("dispatched synthesis", "compositionName", op.Composition.Name, "compositionNamespace", op.Composition.Namespace, "reason", op.Reason)

		if !op.Composition.Synthesizing() {
			inFlight++
		}
	}
	return len(queue) > 0 && len(queue) > deferred, nextDeferredOp
}

func (c *controller) dispatchOp(ctx context.Context, op *op) error {
	patch, err := json.Marshal(op.Patch())
	if err != nil {
		return err
	}
	return c.client.Status().Patch(ctx, op.Composition, client.RawPatch(types.JSONPatchType, patch))
}
