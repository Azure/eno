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
		Named("watchdogController").
		Watches(&apiv1.Composition{}, manager.SingleEventHandler()).
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

	var inputsMissing int
	var notInLockstep int
	var withoutSynthesizers int
	var pendingInit int
	var pending int
	var unready int
	var terminal int
	for _, comp := range list.Items {
		if c.hasNoSynthesizer(&comp, ctx) {
			withoutSynthesizers++
		}
		if c.waitingOnInputs(&comp, ctx) {
			inputsMissing++
		}
		if c.getNotInLockstep(&comp, ctx) {
			notInLockstep++
		}
		if c.pendingInitialReconciliation(&comp) {
			pendingInit++
		}
		if c.pendingReconciliation(&comp) {
			pending++
		}
		if c.pendingReadiness(&comp) {
			unready++
		}
		if c.inTerminalError(&comp) {
			terminal++
		}
	}

	waitingOnInputs.Set(float64(inputsMissing))
	inputsNotInLockstep.Set(float64(notInLockstep))
	compositionsWithoutSynthesizers.Set(float64(withoutSynthesizers))
	pendingInitialReconciliation.Set(float64(pendingInit))
	stuckReconciling.Set(float64(pending))
	pendingReadiness.Set(float64(unready))
	terminalErrors.Set(float64(terminal))

	return ctrl.Result{}, nil
}

func (c *watchdogController) getSynthesizer(comp *apiv1.Composition, ctx context.Context) (*apiv1.Synthesizer, error) {
	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err := c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	return syn, err
}

func (c *watchdogController) hasNoSynthesizer(comp *apiv1.Composition, ctx context.Context) bool {
	_, err := c.getSynthesizer(comp, ctx)
	return err != nil
}

func (c *watchdogController) getInputsExist(comp *apiv1.Composition, ctx context.Context) bool {
	syn, err := c.getSynthesizer(comp, ctx)
	return (err != nil) || comp.InputsExist(syn)
}

func (c *watchdogController) getNotInLockstep(comp *apiv1.Composition, ctx context.Context) bool {
	syn, err := c.getSynthesizer(comp, ctx)
	return (err != nil) || comp.InputsOutOfLockstep(syn)
}

func (c *watchdogController) waitingOnInputs(comp *apiv1.Composition, ctx context.Context) bool {
	return !c.getInputsExist(comp, ctx) && time.Since(comp.CreationTimestamp.Time) > c.threshold
}

func (c *watchdogController) pendingInitialReconciliation(comp *apiv1.Composition) bool {
	return !synthesisHasReconciled(comp.Status.CurrentSynthesis) &&
		!synthesisHasReconciled(comp.Status.PreviousSynthesis) &&
		time.Since(comp.CreationTimestamp.Time) > c.threshold
}

func (c *watchdogController) pendingReconciliation(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil &&
		comp.Status.CurrentSynthesis.Initialized != nil && // important: this is a new CRD property - ignore if nil
		!synthesisHasReconciled(comp.Status.CurrentSynthesis) &&
		time.Since(comp.Status.CurrentSynthesis.Initialized.Time) > c.threshold
}

func (c *watchdogController) pendingReadiness(comp *apiv1.Composition) bool {
	return !synthesisIsReady(comp.Status.CurrentSynthesis) &&
		!synthesisIsReady(comp.Status.PreviousSynthesis) &&
		c.timeSinceReconcilePastThreshold(comp)
}

func (c *watchdogController) inTerminalError(comp *apiv1.Composition) bool {
	synthesis := comp.Status.CurrentSynthesis
	return synthesis != nil && synthesis.Synthesized == nil && synthesis.Failed()
}

func (c *watchdogController) timeSinceReconcilePastThreshold(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && time.Since(comp.Status.CurrentSynthesis.Reconciled.Time) > c.threshold
}

func synthesisHasReconciled(syn *apiv1.Synthesis) bool { return syn != nil && syn.Reconciled != nil }
func synthesisIsReady(syn *apiv1.Synthesis) bool       { return syn != nil && syn.Ready != nil }
