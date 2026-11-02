package tombstonerecovery

import (
	"sync/atomic"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// Persisted recovery decisions, partitioned by synthesizer and reason.
	// Counted only after the decision is written, so retries are not double-counted.
	// Anomaly example: a rising rate of reason="InventoryInvalid" points at corrupted inventory Secrets.
	recoveryDecisions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_tombstone_recovery_decisions_total",
			Help: "Persisted tombstone recovery decisions partitioned by synthesizer and reason",
		},
		[]string{"synthesizer", "reason"},
	)

	// Tombstones written by one completed recovery, including runs that found nothing.
	// Diffs are expected to be small. Anomaly example: observations above 50 (the +Inf bucket)
	// suggest a bad synthesis or the wrong inventory lineage.
	recoveryTombstones = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "eno_tombstone_recovery_tombstones",
			Help:    "Number of tombstones written by a completed tombstone recovery",
			Buckets: []float64{0, 1, 2, 5, 10, 20, 50},
		},
		[]string{"synthesizer"},
	)

	// Time from synthesis completion until the recovery decision is persisted.
	// Reconciliation of the synthesis is blocked for this whole interval.
	recoveryWait = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "eno_tombstone_recovery_wait_seconds",
			Help:    "Time from synthesis completion until the tombstone recovery decision is persisted",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"synthesizer"},
	)

	// Compositions whose reconciliation has been blocked on tombstone recovery longer than
	// --tombstone-recovery-pending-threshold, timed from Synthesized. Alert when this is above zero.
	pendingCompositions = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "eno_tombstone_recovery_pending_compositions",
			Help: "Compositions whose reconciliation has waited on a tombstone recovery decision longer than the pending threshold",
		},
		func() float64 {
			fn := pendingCompositionsFn.Load()
			if fn == nil {
				return 0
			}
			return float64((*fn)())
		},
	)
	// Installed by NewController; nil when tombstone recovery is disabled.
	// atomic.Pointer makes the swap safe against the scrape goroutine.
	pendingCompositionsFn atomic.Pointer[func() int]

	// One series per Composition counted by eno_tombstone_recovery_pending_compositions, so an alert can be traced to specific Compositions.
	pendingCompositionDesc = prometheus.NewDesc(
		"eno_tombstone_recovery_pending_composition",
		"Set to 1 for each Composition whose reconciliation has waited on a tombstone recovery decision longer than the pending threshold",
		[]string{"composition_name", "composition_namespace", "synthesizer"}, nil,
	)
	// Installed by NewController; nil when tombstone recovery is disabled.
	// atomic.Pointer makes the swap safe against the scrape goroutine.
	pendingCompositionDetailsFn atomic.Pointer[func() []apiv1.Composition]
)

func init() {
	metrics.Registry.MustRegister(recoveryDecisions, recoveryTombstones, recoveryWait, pendingCompositions, pendingCompositionCollector{})
}

// setPendingCompositionsSource installs the counter used by the pending compositions gauge. Called once during NewController.
func setPendingCompositionsSource(fn func() int) {
	pendingCompositionsFn.Store(&fn)
}

// setPendingCompositionDetailsSource installs the lister used by the per-Composition pending metric. Called once during NewController.
func setPendingCompositionDetailsSource(fn func() []apiv1.Composition) {
	pendingCompositionDetailsFn.Store(&fn)
}

type pendingCompositionCollector struct{}

func (pendingCompositionCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- pendingCompositionDesc
}

func (pendingCompositionCollector) Collect(ch chan<- prometheus.Metric) {
	fn := pendingCompositionDetailsFn.Load()
	if fn == nil {
		return
	}
	for _, comp := range (*fn)() {
		ch <- prometheus.MustNewConstMetric(pendingCompositionDesc, prometheus.GaugeValue, 1, comp.Name, comp.Namespace, comp.Spec.Synthesizer.Name)
	}
}

func observeRecoveryDecision(comp *apiv1.Composition, reason string) {
	synthesizer := comp.Spec.Synthesizer.Name
	recoveryDecisions.WithLabelValues(synthesizer, reason).Inc()
	if syn := comp.Status.CurrentSynthesis; syn != nil && syn.Synthesized != nil {
		recoveryWait.WithLabelValues(synthesizer).Observe(time.Since(syn.Synthesized.Time).Seconds())
	}
}
