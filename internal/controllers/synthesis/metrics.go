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

	synthesPodRecreations = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_synthesis_pod_recreations_total",
			Help: "Pods deleted due to timeout",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(sytheses, synthesPodRecreations)
}
