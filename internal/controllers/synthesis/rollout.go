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
	client   client.Client
	cooldown time.Duration
}

// NewRolloutController re-synthesizes compositions when their synthesizer has changed while honoring a cooldown period.
func NewRolloutController(mgr ctrl.Manager, cooldownPeriod time.Duration) error {
	c := &rolloutController{
		client:   mgr.GetClient(),
		cooldown: cooldownPeriod,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Synthesizer{}).
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

	if syn.Status.LastRolloutTime != nil {
		remainingCooldown := c.cooldown - time.Since(syn.Status.LastRolloutTime.Time)
		if remainingCooldown > 0 {
			logger.V(1).Info("waiting to roll out a synthesizer change until the cooldown period has passed", "latency", remainingCooldown.Milliseconds())
			return ctrl.Result{RequeueAfter: remainingCooldown}, nil
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

		if comp.Spec.Synthesizer.MinGeneration >= syn.Generation || comp.Status.CurrentState == nil || comp.Status.CurrentState.ObservedSynthesizerGeneration >= syn.Generation {
			continue
		}

		now := metav1.Now()
		syn.Status.LastRolloutTime = &now
		if err := c.client.Status().Update(ctx, syn); err != nil {
			return ctrl.Result{}, fmt.Errorf("advancing last rollout time: %w", err)
		}
		logger.V(1).Info("advanced last rollout time")

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
		logger.Info("synthesizing composition because its synthesizer has changed since last synthesis")
		return ctrl.Result{RequeueAfter: c.cooldown}, nil
	}

	// Update the status to reflect the completed rollout
	if syn.Status.CurrentGeneration != syn.Generation {
		syn.Status.CurrentGeneration = syn.Generation
		if err := c.client.Status().Update(ctx, syn); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating synthesizer's current generation: %w", err)
		}
		logger.Info("rollout is complete - updated synthesizer's current generation")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}
