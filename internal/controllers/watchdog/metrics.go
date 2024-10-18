package watchdog

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	pendingInitialReconciliation = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_pending_initial_reconciliation",
			Help: "Number of compositions that have not been reconciled since a period after their creation",
		},
	)

	pendingReadiness = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_nonready_total",
			Help: "Number of compositions that have not become ready since a period after their reconciliation",
		},
	)

	terminalErrors = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_terminal_error_total",
			Help: "Number of compositions that terminally failed synthesis and will not be retried",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(pendingInitialReconciliation, pendingReadiness, terminalErrors)
}
