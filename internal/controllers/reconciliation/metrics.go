package reconciliation

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	reconciliationLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "eno_reconciliation_duration_seconds",
			Help:    "Samples latency of the entire reconciliation process",
			Buckets: []float64{0.1, 0.5, 0.75, 1.0, 3.0, 6.0, 11.0, 20.0, 30.0, 40.0},
		},
	)

	reconciliationActions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_reconciliation_actions_total",
			Help: "Attempts to reconcile managed resources into the desired state, partitioned by action i.e. create, patch, delete",
		}, []string{"action"},
	)
)

func init() {
	metrics.Registry.MustRegister(reconciliationLatency, reconciliationActions)
}
