package rollout

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// TODO: Move cooldown to global

// TODO: Publish events when initiating synthesis

type synthesizerController struct {
	client client.Client
}

// NewSynthesizerController re-synthesizes compositions when their synthesizer has changed while honoring a cooldown period.
func NewSynthesizerController(mgr ctrl.Manager) error {
	c := &synthesizerController{
		client: mgr.GetClient(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Synthesizer{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToSynthesizerHandler(c.client)).
		WithLogConstructor(manager.NewLogConstructor(mgr, "synthesizerRolloutController")).
		Complete(c)
}

func (c *synthesizerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	syn := &apiv1.Synthesizer{}
	err := c.client.Get(ctx, req.NamespacedName, syn)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting synthesizer: %w", err))
	}
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerNamespace", syn.Namespace, "synthesizerGeneration", syn.Generation)

	compList := &apiv1.CompositionList{}
	err = c.client.List(ctx, compList, client.MatchingFields{
		manager.IdxCompositionsBySynthesizer: syn.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	var lastRolloutTime *metav1.Time
	for _, comp := range compList.Items {
		comp := comp

		// Skip compositions that:
		// - Are brand new (no current synthesis)
		// - Have never received a rolling update (no previous synthesis)
		// - Are currently being synthesized
		if comp.Status.CurrentSynthesis == nil || comp.Status.PreviousSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil {
			continue
		}
		if ts := comp.Status.CurrentSynthesis.Synthesized; lastRolloutTime.Before(ts) {
			lastRolloutTime = ts
		}
	}

	if lastRolloutTime != nil && syn.Spec.RolloutCooldown != nil {
		remainingCooldown := syn.Spec.RolloutCooldown.Duration - time.Since(lastRolloutTime.Time)
		if remainingCooldown > 0 {
			return ctrl.Result{RequeueAfter: remainingCooldown}, nil // not ready to continue rollout yet
		}
	}

	// randomize list to avoid always rolling out changes in the same order
	rand.Shuffle(len(compList.Items), func(i, j int) { compList.Items[i] = compList.Items[j] })

	for _, comp := range compList.Items {
		comp := comp
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

		// Compositions aren't eligible to receive an updated synthesizer when:
		// - They are newer than the cooldown period
		// - They haven't ever been synthesized (they'll use the latest inputs anyway)
		// - They are currently being synthesized
		// - They are already in sync with the latest inputs
		if time.Since(comp.CreationTimestamp.Time) < syn.Spec.RolloutCooldown.Duration || comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil || isInSync(&comp, syn) {
			continue
		}

		swapStates(&comp)
		err = c.client.Status().Update(ctx, &comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}

		logger.V(1).Info("advancing rollout process")
		return ctrl.Result{RequeueAfter: syn.Spec.RolloutCooldown.Duration}, nil
	}

	return ctrl.Result{}, nil
}

func swapStates(comp *apiv1.Composition) {
	// If the previous state has been synthesized but not the current, keep the previous to avoid orphaning deleted resources
	if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil {
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
	}

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
	}
}

func isInSync(comp *apiv1.Composition, syn *apiv1.Synthesizer) bool {
	return comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration >= syn.Generation
}
