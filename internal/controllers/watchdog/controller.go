package watchdog

import (
	"context"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/prometheus/client_golang/prometheus"
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

	accumulators := []*accumulator{
		{
			Predicate: c.waitingOnInputs,
			Sink:      inputsMissing,
		},
		{
			Predicate: c.pendingReconciliation,
			Sink:      stuckReconciling,
		},
		{
			Predicate: c.pendingReadiness,
			Sink:      nonready,
		},
		{
			Predicate: c.inTerminalError,
			Sink:      terminalErrors,
		},
	}

	for _, comp := range list.Items {
		for _, a := range accumulators {
			a.Visit(&comp)
		}
	}

	return ctrl.Result{}, nil
}

func (c *watchdogController) waitingOnInputs(comp *apiv1.Composition) bool {
	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err := c.client.Get(context.Background(), client.ObjectKeyFromObject(syn), syn)
	return err == nil && (!comp.InputsExist(syn) || comp.InputsOutOfLockstep(syn))
}

func (c *watchdogController) pendingReconciliation(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil &&
		comp.Status.CurrentSynthesis.Initialized != nil && // important: this is a new CRD property - ignore if nil
		!synthesisHasReconciled(comp.Status.CurrentSynthesis) &&
		time.Since(comp.Status.CurrentSynthesis.Initialized.Time) > c.threshold
}

func (c *watchdogController) pendingReadiness(comp *apiv1.Composition) bool {
	return !synthesisIsReady(comp.Status.CurrentSynthesis) &&
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

type accumulator struct {
	Predicate func(*apiv1.Composition) bool
	Sink      *prometheus.GaugeVec
	init      bool
}

func (a *accumulator) Visit(source *apiv1.Composition) {
	if !a.init {
		a.Sink.Reset()
		a.init = true
	}
	if a.Predicate(source) {
		a.Sink.WithLabelValues(source.Spec.Synthesizer.Name).Add(1)
	} else {
		a.Sink.WithLabelValues(source.Spec.Synthesizer.Name).Add(0)
	}
}
