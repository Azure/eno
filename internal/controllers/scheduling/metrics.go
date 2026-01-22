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

	compositionStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_composition_status",
			Help: "Status of composition reconciliation (1 = stuck, 0 = healthy)",
		}, []string{"composition_name", "composition_namespace", "synthesizer_name"},
	)
)

func init() {
	metrics.Registry.MustRegister(freeSynthesisSlots, schedulingLatency, stuckReconciling, compositionStatus)
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
