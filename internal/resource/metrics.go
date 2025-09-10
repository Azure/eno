package resource

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	resourceFilterErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_resource_filter_eval_errors_total",
			Help: "Errors while evaluating resource filter expressions",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(resourceFilterErrors)
}
