package watchdog

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	waitingOnInputs = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_inputs_missing_total",
			Help: "Number of compositions that are missing input resources",
		},
	)

	inputsNotInLockstep = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_inputs_not_in_lockstep_total",
			Help: "Number of compositions that have input resources that are not in lockstep with the composition's current state",
		},
	)

	compositionsWithoutSynthesizers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_without_synthesizers_total",
			Help: "Number of compositions that do not have synthesizers",
		},
	)

	pendingInitialReconciliation = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_pending_initial_reconciliation",
			Help: "Number of compositions that have not been reconciled since a period after their creation",
		},
	)

	stuckReconciling = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "eno_compositions_stuck_reconciling_total",
			Help: "Number of compositions that have not been reconciled since a period after their current synthesis was initialized",
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
	metrics.Registry.MustRegister(waitingOnInputs, inputsNotInLockstep, compositionsWithoutSynthesizers, pendingInitialReconciliation, stuckReconciling, pendingReadiness, terminalErrors)
}
