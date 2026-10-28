package scheduling

import (
	"path"
	"sync/atomic"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/inputs"
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	stuckReasonDeleting              = "Deleting"
	stuckReasonMissingSynthesizer    = "MissingSynthesizer"
	stuckReasonCircularDependency    = apiv1.CircularDependencyReason
	stuckReasonWaitingOnDependencies = apiv1.WaitingOnDependenciesReason
	stuckReasonMissingInputs         = "MissingInputs"
	stuckReasonMismatchedInputs      = "MismatchedInputs"
	stuckReasonSynthesisBackoff      = "SynthesisBackoff"
	stuckReasonSynthesizing          = "Synthesizing"
	stuckReasonPendingSynthesis      = "PendingSynthesis"
	stuckReasonTombstoneRecovery     = "TombstoneRecovery"
	stuckReasonReconciling           = "Reconciling"
	stuckReasonNotReady              = "NotReady"
)

var (
	// Stuck compositions per synthesizer and reason. Absent series mean zero.
	// Anomaly example: reason="SynthesisBackoff" rising for one synthesizer points at a failing synthesizer image.
	compositionsStuckDesc = prometheus.NewDesc(
		"eno_compositions_stuck",
		"Number of compositions stuck in one phase longer than the watchdog threshold, partitioned by synthesizer, owner, and reason",
		[]string{"synthesizer", "owner", "reason"}, nil,
	)

	// One series per stuck composition; healthy compositions have no series.
	compositionStuckReasonDesc = prometheus.NewDesc(
		"eno_composition_stuck_reason",
		"Set to 1 for each composition stuck in one phase longer than the watchdog threshold, labeled with the reason",
		[]string{"composition_name", "composition_namespace", "synthesizer_name", "reason"}, nil,
	)

	stuckCompositionsSnapshot atomic.Pointer[[]stuckComposition]
)

func init() {
	metrics.Registry.MustRegister(stuckCompositionsCollector{})
}

type stuckComposition struct {
	name        string
	namespace   string
	synthesizer string
	owner       string
	reason      string
}

// planStuckCompositions returns every composition stuck in its current phase for longer than threshold.
func planStuckCompositions(comps []apiv1.Composition, synths map[string]apiv1.Synthesizer, readySet, cyclicSet map[string]bool, threshold time.Duration, now time.Time) []stuckComposition {
	var stuck []stuckComposition
	for i := range comps {
		comp := &comps[i]
		synth, found := synths[comp.Spec.Synthesizer.Name]
		var synthPtr *apiv1.Synthesizer
		if found {
			synthPtr = &synth
		}
		reason, since := classifyComposition(comp, synthPtr, readySet, cyclicSet)
		if reason == "" || now.Sub(since) <= threshold {
			continue
		}
		stuck = append(stuck, stuckComposition{
			name:        comp.Name,
			namespace:   comp.Namespace,
			synthesizer: comp.Spec.Synthesizer.Name,
			owner:       getSynthOwner(synthPtr),
			reason:      reason,
		})
	}
	return stuck
}

// classifyComposition returns the phase a composition is waiting in and when that phase started.
// The first matching check wins. An empty reason means the composition is Ready.
func classifyComposition(comp *apiv1.Composition, synth *apiv1.Synthesizer, readySet, cyclicSet map[string]bool) (string, time.Time) {
	created := comp.CreationTimestamp.Time
	if comp.DeletionTimestamp != nil {
		return stuckReasonDeleting, comp.DeletionTimestamp.Time
	}
	if synth == nil {
		return stuckReasonMissingSynthesizer, created
	}
	if cyclicSet[path.Join(comp.Namespace, comp.Name)] {
		return stuckReasonCircularDependency, created
	}

	current, inFlight := comp.Status.CurrentSynthesis, comp.Status.InFlightSynthesis
	// Input and dependency problems have no transition timestamp, so they are only attributed
	// while the first synthesis is blocked and the creation time is a meaningful start.
	if current == nil {
		if len(comp.Spec.DependsOn) > 0 && !areDependenciesReady(comp, readySet) {
			return stuckReasonWaitingOnDependencies, created
		}
		if !inputs.Exist(synth, comp) {
			return stuckReasonMissingInputs, created
		}
		if inputs.OutOfLockstep(synth, comp, comp.Status.InputRevisions) {
			return stuckReasonMismatchedInputs, created
		}
	}

	if inFlight != nil {
		if inFlight.Canceled != nil {
			return stuckReasonSynthesisBackoff, inFlight.Canceled.Time
		}
		return stuckReasonSynthesizing, firstSetTime(created, inFlight.Initialized)
	}
	if current == nil {
		return stuckReasonPendingSynthesis, created
	}
	if current.Reconciled == nil {
		since := firstSetTime(created, current.Synthesized, current.Initialized)
		// Mirrors the reconstitution gate that blocks reconciliation until recovery finishes.
		if comp.RecoveryEnabled() && current.Synthesized != nil && !current.IsTombstoneRecoveryFinished() {
			return stuckReasonTombstoneRecovery, since
		}
		return stuckReasonReconciling, since
	}
	if current.Ready == nil {
		return stuckReasonNotReady, current.Reconciled.Time
	}
	return "", time.Time{}
}

func firstSetTime(fallback time.Time, times ...*metav1.Time) time.Time {
	for _, t := range times {
		if t != nil {
			return t.Time
		}
	}
	return fallback
}

// publishStuckCompositions swaps the whole snapshot so scrapes never observe a partial update.
func publishStuckCompositions(stuck []stuckComposition) {
	stuckCompositionsSnapshot.Store(&stuck)
}

type stuckCompositionsCollector struct{}

func (stuckCompositionsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- compositionsStuckDesc
	ch <- compositionStuckReasonDesc
}

func (stuckCompositionsCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := stuckCompositionsSnapshot.Load()
	if snapshot == nil {
		return
	}
	type groupKey struct {
		synthesizer string
		owner       string
		reason      string
	}
	counts := map[groupKey]int{}
	for _, s := range *snapshot {
		counts[groupKey{s.synthesizer, s.owner, s.reason}]++
		ch <- prometheus.MustNewConstMetric(compositionStuckReasonDesc, prometheus.GaugeValue, 1, s.name, s.namespace, s.synthesizer, s.reason)
	}
	for k, count := range counts {
		ch <- prometheus.MustNewConstMetric(compositionsStuckDesc, prometheus.GaugeValue, float64(count), k.synthesizer, k.owner, k.reason)
	}
}
