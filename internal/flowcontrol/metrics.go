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

	// Depth of the composition input-revision write buffer's internal queue. A
	// persistently non-zero value means input-revision patches are accumulating
	// faster than the buffer can flush them to apiserver, which directly delays
	// downstream resynthesis.
	inputRevisionBufferDepth = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "eno_input_revision_write_buffer_depth",
			Help: "Current depth of the composition input-revision status write buffer queue",
		},
		func() float64 {
			fn := inputRevisionBufferLenFn.Load()
			if fn == nil {
				return 0
			}
			return float64((*fn)())
		},
	)
	// Closure that returns the current input-revision write buffer queue depth.
	// Installed by NewCompositionInputRevisionWriteBuffer; nil before the buffer is
	// constructed. atomic.Pointer makes the swap safe against the scrape goroutine.
	inputRevisionBufferLenFn atomic.Pointer[func() int]

	// Errors hit while flushing input-revision patches, partitioned by op (get/patch).
	// These silently retry today, so without this counter they're invisible.
	inputRevisionBufferFlushErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "eno_input_revision_write_buffer_flush_errors_total",
			Help: "Errors encountered while flushing composition input revision updates, partitioned by operation",
		}, []string{"op"},
	)

	// Count of successful flushes of composition input-revision updates.
	inputRevisionBufferFlushes = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "eno_input_revision_write_buffer_flush_total",
			Help: "Count of successful flushes of composition input revision updates",
		},
	)
)

func init() {
	metrics.Registry.MustRegister(sliceStatusUpdates, writeBufferDepth, writeBufferStatusUpdateErrors,
		inputRevisionBufferDepth, inputRevisionBufferFlushErrors, inputRevisionBufferFlushes)
}

// setWriteBufferLenSource installs the queue.Len reader used by the depth gauge.
// Called once during NewResourceSliceWriteBuffer.
func setWriteBufferLenSource(fn func() int) {
	writeBufferLenFn.Store(&fn)
}

// setInputRevisionBufferLenSource installs the queue.Len reader used by the input-revision
// buffer's depth gauge. Called once during NewCompositionInputRevisionWriteBuffer.
func setInputRevisionBufferLenSource(fn func() int) {
	inputRevisionBufferLenFn.Store(&fn)
}
