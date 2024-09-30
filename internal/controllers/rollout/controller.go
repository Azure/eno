package rollout

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/manager"
)

type controller struct {
	client   client.Client
	cooldown time.Duration
}

func NewController(mgr ctrl.Manager, cooldown time.Duration) error {
	c := &controller{
		client:   mgr.GetClient(),
		cooldown: cooldown,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("rolloutController").
		Watches(&apiv1.Composition{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "rolloutController")).
		Complete(c)
}

func (c *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comps := &apiv1.CompositionList{}
	err := c.client.List(ctx, comps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	// Find the current cooldown period, wait for the next if needed
	// Block on any active deferred syntheses with less than 2 attempts to avoid high concurrency
	var lastDeferredSynthesisCompletion *metav1.Time
	for _, comp := range comps.Items {
		current := comp.Status.CurrentSynthesis
		if current == nil || !current.Deferred {
			continue
		}
		if current.Synthesized == nil && current.Attempts < 2 {
			return ctrl.Result{RequeueAfter: c.cooldown}, nil
		}
		if lastDeferredSynthesisCompletion == nil || lastDeferredSynthesisCompletion.Before(current.Synthesized) {
			lastDeferredSynthesisCompletion = current.Synthesized
		}
	}
	if lastDeferredSynthesisCompletion != nil {
		delta := c.cooldown - time.Since(lastDeferredSynthesisCompletion.Time)
		if delta > 0 {
			return ctrl.Result{RequeueAfter: delta}, nil
		}
	}

	// Cancel pending resynthesis for compositions that are ignoring side effects.
	// Do this before calculating selecting the next composition to avoid
	// skewing the results.
	for _, comp := range comps.Items {
		if comp.Status.PendingResynthesis != nil && comp.ShouldIgnoreSideEffects() {
			comp.Status.PendingResynthesis = nil
			err := c.client.Status().Update(ctx, &comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("clearing PendingResythesis field for %s in namespace %s: %w", comp.Name, comp.Namespace, err)
			}
			return ctrl.Result{}, nil
		}
	}

	// Sort for FIFO
	sort.Slice(comps.Items, func(i, j int) bool {
		return ptr.Deref(comps.Items[j].Status.PendingResynthesis, metav1.Time{}).After(ptr.Deref(comps.Items[i].Status.PendingResynthesis, metav1.Time{}).Time)
	})

	for _, comp := range comps.Items {
		logger := logger.WithValues("compositionName", comp.Name,
			"compositionNamespace", comp.Namespace,
			"compositionGeneration", comp.Generation,
			"synthesisID", comp.Status.GetCurrentSynthesisUUID())
		if comp.Status.PendingResynthesis == nil || comp.Status.CurrentSynthesis == nil {
			continue
		}

		// Guarantee that we don't violate the cooldown period in the case of stale informers
		if comp.Status.CurrentSynthesis.Deferred {
			delta := c.cooldown - time.Since(comp.Status.PendingResynthesis.Time)
			if delta > 0 {
				return ctrl.Result{RequeueAfter: delta}, nil
			}
		}

		pendingTime := comp.Status.PendingResynthesis
		synthesis.SwapStates(&comp)
		comp.Status.CurrentSynthesis.Deferred = true
		comp.Status.PendingResynthesis = nil
		err = c.client.Status().Update(ctx, &comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("initiating resynthesis: %w", err)
		}

		logger.Info("progressing deferred resynthesis", "latency", time.Since(pendingTime.Time).Milliseconds())
		return ctrl.Result{RequeueAfter: c.cooldown}, nil
	}

	// drop the work item until a composition changes
	return ctrl.Result{}, nil
}
