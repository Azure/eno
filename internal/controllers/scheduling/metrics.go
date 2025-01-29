package scheduling

import (
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
)

func init() {
	metrics.Registry.MustRegister(freeSynthesisSlots, schedulingLatency)
}
