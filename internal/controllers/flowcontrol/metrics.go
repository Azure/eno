package flowcontrol

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	pendingSyntheses = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_pending_syntheses_total",
			Help: "Count of the syntheses that are being deferred by a flow control mechanism",
		},
	)
	activeSyntheses = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_active_syntheses_total",
			Help: "Count of the syntheses that are being synthesized",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(pendingSyntheses)
	metrics.Registry.MustRegister(activeSyntheses)
}
