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
	// Tracks the outcome of every synthesis attempt, broken down per synthesizer.
	// Used to compute success rate and to alert when a specific synthesizer starts
	// failing. Anomaly example: a sudden spike in `result="timeout"` or
	// `result="kubeletTimeout"` for one synthesizer indicates the synth pod is
	// hanging or the node is unhealthy, while the rest of the fleet looks fine.
	synthesisResults = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_synthesis_result_total",
			Help: "Synthesis operations partitioned by synthesizer and result",
		},
		[]string{"synthesizer", "result"},
	)

	// Wall-clock latency from pod creation to terminal state, per synthesizer and
	// result. Used to track p95/p99 synthesis latency and detect regressions in
	// synthesizer performance or scheduling. Anomaly example: p95 for a given
	// synthesizer jumps from ~5s into the 60-120s bucket after a release, pointing
	// at a slow synthesizer image or kubelet pull/start delays.
	synthesisDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "eno_synthesis_duration_seconds",
			Help:    "Wall-clock duration for synthesizer pod creation to terminal state",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600},
		},
		[]string{"synthesizer", "result"},
	)
)

func init() {
	metrics.Registry.MustRegister(sytheses, synthesisResults, synthesisDuration)
}
