package synthesis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type rolloutController struct {
	client client.Client
}

// NewRolloutController updates composition statuses as pods transition through states.
func NewRolloutController(mgr ctrl.Manager) error {
	c := &rolloutController{
		client: mgr.GetClient(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Synthesizer{}).
		Watches(&apiv1.Composition{}, enqueueSynthesizerFromCompositions(c.client)).
		WithLogConstructor(manager.NewLogConstructor(mgr, "rolloutController")).
		Complete(c)
}

func (c *rolloutController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	syn := &apiv1.Synthesizer{}
	err := c.client.Get(ctx, req.NamespacedName, syn)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting synthesizer: %w", err))
	}

	compList := &apiv1.CompositionList{}
	err = c.client.List(ctx, compList, client.MatchingFields{
		manager.IdxCompositionsBySynthesizer: syn.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	cooldown, wait := waitForCooldown(syn, compList, time.Millisecond) // TODO: Configure
	for _, comp := range compList.Items {
		comp := comp
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

		// - Always re-synthesize when the composition has changed
		// - Slow roll synthesizer changes across their compositions
		compHasChanged := compositionChangedSinceLastSynthesis(&comp)
		synHasChanged := synthesizerChangedSinceLastSynthesis(syn, &comp)
		if compHasChanged || (synHasChanged && !wait) {
			if compHasChanged {
				logger.Info("synthesizing composition because it has changed since last synthesis")
			} else if synHasChanged && !wait {
				logger.Info("waiting for cooldown before updating composition to latest synthesizer")
			} else if synHasChanged {
				logger.Info("synthesizing composition because its synthesizer has changed since last synthesis")
			}

			swapStates(syn, &comp)
			return ctrl.Result{}, c.client.Status().Update(ctx, &comp)
		}
	}

	return ctrl.Result{RequeueAfter: cooldown}, nil
}

func swapStates(syn *apiv1.Synthesizer, comp *apiv1.Composition) {
	// Only swap current->previous when the current synthesis has completed
	// This avoids losing the prior state during rapid updates to the composition
	resourceSliceCountSet := comp.Status.CurrentState != nil && comp.Status.CurrentState.ResourceSliceCount != nil
	if resourceSliceCountSet {
		comp.Status.PreviousState = comp.Status.CurrentState
	}

	comp.Status.CurrentState = &apiv1.Synthesis{
		ObservedGeneration: comp.Generation,
	}
}

func compositionChangedSinceLastSynthesis(comp *apiv1.Composition) bool {
	return comp.Status.CurrentState == nil || comp.Status.CurrentState.ObservedGeneration != comp.Generation
}

func synthesizerChangedSinceLastSynthesis(syn *apiv1.Synthesizer, comp *apiv1.Composition) bool {
	return comp.Status.CurrentState != nil &&
		comp.Status.CurrentState.ObservedSynthesizerGeneration != nil &&
		*comp.Status.CurrentState.ObservedSynthesizerGeneration != syn.Generation
}

func waitForCooldown(syn *apiv1.Synthesizer, compList *apiv1.CompositionList, cooldown time.Duration) (time.Duration, bool) {
	lastCreation, ok := findLastPodCreation(syn, compList)
	t := time.Since(lastCreation)
	return t, !ok || t < cooldown
}

func findLastPodCreation(syn *apiv1.Synthesizer, compList *apiv1.CompositionList) (t time.Time, ok bool) {
	for _, item := range compList.Items {
		if item.Status.CurrentState != nil && item.Status.CurrentState.PodCreation != nil && item.Status.CurrentState.PodCreation.Time.After(t) {
			t = item.Status.CurrentState.PodCreation.Time
			ok = true
		}
	}
	return t, ok
}
