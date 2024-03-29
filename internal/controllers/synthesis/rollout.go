package synthesis

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type rolloutController struct {
	client client.Client
}

// NewRolloutController re-synthesizes compositions when their synthesizer has changed while honoring a cooldown period.
func NewRolloutController(mgr ctrl.Manager) error {
	c := &rolloutController{
		client: mgr.GetClient(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Synthesizer{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToSynthesizerHandler(c.client)).
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
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerNamespace", syn.Namespace, "synthesizerGeneration", syn.Generation)

	if syn.Status.LastRolloutTime != nil && syn.Status.CurrentGeneration != syn.Generation {
		if syn.Spec.RolloutCooldown == nil {
			return ctrl.Result{}, nil // not configured
		}
		remainingCooldown := syn.Spec.RolloutCooldown.Duration - time.Since(syn.Status.LastRolloutTime.Time)
		if remainingCooldown > 0 {
			return ctrl.Result{RequeueAfter: remainingCooldown}, nil // not ready to continue rollout yet
		}
	}

	compList := &apiv1.CompositionList{}
	err = c.client.List(ctx, compList, client.MatchingFields{
		manager.IdxCompositionsBySynthesizer: syn.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	// randomize list to avoid always rolling out changes in the same order
	rand.Shuffle(len(compList.Items), func(i, j int) { compList.Items[i] = compList.Items[j] })

	for _, comp := range compList.Items {
		comp := comp
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

		// Compositions aren't eligible to receive an updated synthesizer when:
		// - They are newer than the cooldown period
		// - They already use this or a newer synthesizer version
		// - They haven't ever been synthesized (they'll use the new synthesizer version anyway)
		// - They are currently being synthesized
		// - They've been synthesized by this or a newer version
		if time.Since(comp.CreationTimestamp.Time) < syn.Spec.RolloutCooldown.Duration || comp.Spec.Synthesizer.MinGeneration >= syn.Generation || comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil || comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration >= syn.Generation {
			continue
		}

		now := metav1.Now()
		syn.Status.LastRolloutTime = &now
		if err := c.client.Status().Update(ctx, syn); err != nil {
			return ctrl.Result{}, fmt.Errorf("advancing last rollout time: %w", err)
		}

		err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := c.client.Get(ctx, client.ObjectKeyFromObject(&comp), &comp); err != nil {
				return err
			}
			comp.Spec.Synthesizer.MinGeneration = syn.Generation
			return c.client.Update(ctx, &comp)
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}
		logger.V(0).Info("advancing synthesizer rollout process")
		return ctrl.Result{RequeueAfter: syn.Spec.RolloutCooldown.Duration}, nil
	}

	// Update the status to reflect the completed rollout
	if syn.Status.CurrentGeneration != syn.Generation {
		previousTime := syn.Status.LastRolloutTime
		now := metav1.Now()
		syn.Status.LastRolloutTime = &now
		syn.Status.CurrentGeneration = syn.Generation
		if err := c.client.Status().Update(ctx, syn); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating synthesizer's current generation: %w", err)
		}

		if previousTime != nil {
			logger = logger.WithValues("latency", now.Sub(previousTime.Time).Milliseconds())
		}
		if len(compList.Items) > 0 { // log doesn't make sense if the synthesizer wasn't actually rolled out
			logger.V(0).Info("finished rolling out latest synthesizer version")
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}
