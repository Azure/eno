package flowcontrol

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	sliceStatusUpdates = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_resource_slice_status_update_total",
			Help: "Count of batch updates to resource slice status",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(sliceStatusUpdates)
}
