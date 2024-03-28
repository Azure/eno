package watchdog

import (
	"context"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// watchdogController exposes metrics that track the states of Eno resources relative to the current time.
// The idea is to identify deadlock states so they can be alerted on.
type watchdogController struct {
	client    client.Client
	threshold time.Duration
}

func NewController(mgr ctrl.Manager, threshold time.Duration) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "watchdogController")).
		Complete(&watchdogController{
			client:    mgr.GetClient(),
			threshold: threshold,
		})
}

func (c *watchdogController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	list := &apiv1.CompositionList{}
	err := c.client.List(ctx, list)
	if err != nil {
		return ctrl.Result{}, err
	}

	var unrecd int
	var unready int
	for _, comp := range list.Items {
		if c.pendingReconciliation(&comp) {
			unrecd++
		}
		if c.pendingReadiness(&comp) {
			unready++
		}
	}

	pendingReconciliation.Set(float64(unrecd))
	pendingReadiness.Set(float64(unready))

	return ctrl.Result{}, nil
}

func (c *watchdogController) pendingReconciliation(comp *apiv1.Composition) bool {
	return !synthesisHasReconciled(comp.Status.CurrentSynthesis) &&
		!synthesisHasReconciled(comp.Status.PreviousSynthesis) &&
		time.Since(comp.CreationTimestamp.Time) > c.threshold
}

func (c *watchdogController) pendingReadiness(comp *apiv1.Composition) bool {
	return !synthesisIsReady(comp.Status.CurrentSynthesis) &&
		!synthesisIsReady(comp.Status.PreviousSynthesis) &&
		c.timeSinceReconcilePastThreshold(comp)
}

func (c *watchdogController) timeSinceReconcilePastThreshold(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && time.Since(comp.Status.CurrentSynthesis.Reconciled.Time) > c.threshold
}

func synthesisHasReconciled(syn *apiv1.Synthesis) bool { return syn != nil && syn.Reconciled != nil }
func synthesisIsReady(syn *apiv1.Synthesis) bool       { return syn != nil && syn.Ready != nil }
