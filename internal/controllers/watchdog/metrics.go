package watchdog

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	inputsMissing = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_compositions_inputs_missing_total",
			Help: "Number of compositions that are unable to be synthesized due to the state of their inputs",
		}, []string{"synthesizer"},
	)

	stuckReconciling = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_compositions_stuck_reconciling_total",
			Help: "Number of compositions that have not been reconciled since a period after their current synthesis was initialized",
		}, []string{"synthesizer"},
	)

	nonready = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_compositions_nonready_total",
			Help: "Number of compositions that have not become ready since a period after their reconciliation",
		}, []string{"synthesizer"},
	)

	terminalErrors = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "eno_compositions_terminal_error_total",
			Help: "Number of compositions that terminally failed synthesis and will not be retried",
		}, []string{"synthesizer"},
	)
)

func init() {
	metrics.Registry.MustRegister(inputsMissing, stuckReconciling, nonready, terminalErrors)
}
