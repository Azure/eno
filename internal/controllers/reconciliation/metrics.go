package reconciliation

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	reconciliationLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "eno_reconciliation_duration_seconds",
			Help:    "Latency of the entire reconciliation process, in seconds",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
		},
	)

	reconciliationActions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_reconciliation_actions_total",
			Help: "Attempts to reconcile managed resources into the desired state, partitioned by action i.e. create, patch, delete",
		}, []string{"action"},
	)

	reconciliationWorkqueueDepth = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "eno_reconciliation_workqueue_depth",
			Help: "Current depth of the reconciliation controller workqueue",
		},
		func() float64 {
			fn := workqueueLenFn.Load()
			if fn == nil {
				return 0
			}
			return float64((*fn)())
		},
	)
	// Closure that returns the current reconciler workqueue depth. Installed by
	// the Controller's NewQueue hook (see controller.go); nil before the queue is
	// constructed. atomic.Pointer makes the swap safe against the scrape goroutine.
	workqueueLenFn atomic.Pointer[func() int]
)

func init() {
	metrics.Registry.MustRegister(reconciliationLatency, reconciliationActions, reconciliationWorkqueueDepth)
}

// setWorkqueueLenSource installs the queue.Len reader used by the gauge above.
// this is called once during Controller construction
func setWorkqueueLenSource(fn func() int) {
	workqueueLenFn.Store(&fn)
}
