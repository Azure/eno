package rollout

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type controller struct {
	client   client.Client
	cooldown time.Duration
}

// NewController re-synthesizes compositions when their synthesizer has changed while honoring a cooldown period.
func NewController(mgr ctrl.Manager, cooldown time.Duration) error {
	c := &controller{
		client:   mgr.GetClient(),
		cooldown: cooldown,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Synthesizer{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToSynthesizerHandler(c.client)).
		// TODO: Filter some events?
		WithLogConstructor(manager.NewLogConstructor(mgr, "synthesizerRolloutController")).
		Complete(c)
}

func (c *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	var latestRollout time.Time
	for _, comp := range compList.Items {
		// For every synthesizer, only one composition can be the target of a rolling change at any point in time.
		// To avoid blocked rollouts caused by compositions that cannot be synthesized, also time out and move on eventually.
		if isRolling(&comp) {
			return ctrl.Result{}, nil
		}

		if comp.Status.CurrentSynthesis != nil && comp.Status.PreviousSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil && comp.Status.CurrentSynthesis.Synthesized.Time.After(latestRollout) {
			latestRollout = comp.Status.CurrentSynthesis.Synthesized.Time
		}
	}
	if delta := time.Since(latestRollout); delta < c.cooldown {
		return ctrl.Result{RequeueAfter: c.cooldown - delta}, nil
	}

	// randomize list to avoid always rolling out changes in the same order
	rand.Shuffle(len(compList.Items), func(i, j int) { compList.Items[i] = compList.Items[j] })

	for _, comp := range compList.Items {
		comp := comp
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

		// Compositions aren't eligible to receive an updated synthesizer when:
		// - They haven't ever been synthesized (they'll use the latest inputs anyway)
		// - They are currently being synthesized
		// - They are already in sync with the latest inputs
		if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil || isInSync(&comp, syn) {
			continue
		}

		swapStates(&comp)
		err = c.client.Status().Update(ctx, &comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}

		logger.V(1).Info("advancing rollout process")
		return ctrl.Result{}, nil
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

func isRolling(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && comp.Status.PreviousSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized == nil
}
