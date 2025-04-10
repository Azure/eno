package synthesis

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	sytheses = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_syntheses_total",
			Help: "Initiated synthesis operations",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(sytheses)
}
