package scheduling

import (
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	freeSynthesisSlots = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_free_synthesis_slots",
			Help: "Count of how many syntheses could be dispatched concurrently",
		},
	)

	schedulingLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "eno_scheduling_latency_seconds",
			Help:    "Latency of scheduling operations",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 5},
		},
	)

	stuckReconciling = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_compositions_stuck_reconciling_total",
			Help: "Number of compositions that have not been reconciled since a period after their current synthesis was initialized",
		}, []string{"synthesizer", "owner"},
	)

	compositionHealth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_composition_health",
			Help: "Health status of each composition (0 = healthy, 1 = stuck/unhealthy)",
		}, []string{"composition_name", "composition_namespace", "synthesizer_name"},
	)

	// Per-composition wait between Synthesis.Initialized (the moment the controller
	// decided this composition should be synthesized) and successful creation of
	// the synthesizer pod. Captures customer-visible queueing latency that is
	// independent of synthesizer pod runtime. Anomaly example: p95 jumps from
	// sub-second into the 60-300s bucket while `eno_free_synthesis_slots` is
	// occasionally non-zero — points at a dispatch-loop stall or apiserver
	// contention rather than raw capacity exhaustion.
	synthesisDispatchWait = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "eno_composition_synthesis_wait_seconds",
			Help:    "Time from Synthesis.Initialized to successful pod creation",
			Buckets: []float64{0.1, 0.5, 1, 5, 15, 60, 300, 900},
		},
	)
)

func init() {
	metrics.Registry.MustRegister(freeSynthesisSlots, schedulingLatency, stuckReconciling, compositionHealth, synthesisDispatchWait)
}

func missedReconciliation(comp *apiv1.Composition, threshold time.Duration) bool {
	syn := comp.Status.CurrentSynthesis
	if comp.DeletionTimestamp != nil {
		return time.Since(comp.DeletionTimestamp.Time) > threshold // stuck deleting
	}
	if syn != nil && syn.Reconciled != nil {
		return false // reconciled!
	}
	if (syn == nil || syn.Initialized == nil) && comp.Status.PreviousSynthesis == nil && time.Since(comp.CreationTimestamp.Time) > threshold {
		return true // stuck waiting for synthesis dispatch
	}
	return syn != nil && syn.Initialized != nil && time.Since(syn.Initialized.Time) > threshold // stuck waiting for reconciliation
}

func ObserveSynthesisDispatchWait(seconds float64) {
	synthesisDispatchWait.Observe(seconds)
}
