package synthesis

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	synthesisLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "eno_synthesis_duration_seconds",
			Help:    "Samples the time between starting and completing the synthesis of a composition",
			Buckets: []float64{0.5, 1.0, 2.0, 3.0, 5.0, 8.0, 10.0},
		},
	)

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

	resourceSliceWrittenBytes = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_resource_slice_written_bytes_total",
			Help: "Manifest bytes written to ResourceSlice resources",
		},
	)

	synthesisExecFailures = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_synthesis_exec_errors_total",
			Help: "Errors exec'ing into synthesizer pods",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(synthesisLatency, sytheses, synthesPodRecreations, resourceSliceWrittenBytes, synthesisExecFailures)
}
