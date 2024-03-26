package readiness

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	celEvalCost = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_readiness_eval_cost_total",
			Help: "Total cost of all evaluated CEL readiness expressions",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(celEvalCost)
}
