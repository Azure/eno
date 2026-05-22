package flowcontrol

import (
	"sync/atomic"

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

	// Depth of the write buffer's internal queue. A persistently non-zero value
	// means status patches are accumulating faster than the buffer can flush them
	// to apiserver, which directly delays comp.Status.{Reconciled,Ready} updates.
	writeBufferDepth = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "eno_write_buffer_depth",
			Help: "Current depth of the resource slice status write buffer queue",
		},
		func() float64 {
			fn := writeBufferLenFn.Load()
			if fn == nil {
				return 0
			}
			return float64((*fn)())
		},
	)
	// Closure that returns the current write buffer queue depth. Installed by
	// NewResourceSliceWriteBuffer; nil before the buffer is constructed.
	// atomic.Pointer makes the swap safe against the scrape goroutine.
	writeBufferLenFn atomic.Pointer[func() int]

	// Errors hit while flushing status patches. Partitioned by op (get/patch/marshal)
	// to distinguish "stale cache" (get) from "apiserver rejected the patch" (patch).
	// All of these silently retry today, so without this counter they're invisible.
	writeBufferStatusUpdateErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_write_buffer_status_update_errors_total",
			Help: "Errors encountered while flushing resource slice status updates, partitioned by operation",
		}, []string{"op"},
	)
)

func init() {
	metrics.Registry.MustRegister(sliceStatusUpdates, writeBufferDepth, writeBufferStatusUpdateErrors)
}

// setWriteBufferLenSource installs the queue.Len reader used by the depth gauge.
// Called once during NewResourceSliceWriteBuffer.
func setWriteBufferLenSource(fn func() int) {
	writeBufferLenFn.Store(&fn)
}
