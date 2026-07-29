package flowcontrol

import (
	"testing"

	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestWriteBufferStatusUpdateErrorsLabels(t *testing.T) {
	writeBufferStatusUpdateErrors.Reset()

	writeBufferStatusUpdateErrors.WithLabelValues("get").Inc()
	writeBufferStatusUpdateErrors.WithLabelValues("get").Inc()
	writeBufferStatusUpdateErrors.WithLabelValues("marshal").Inc()
	writeBufferStatusUpdateErrors.WithLabelValues("patch").Inc()

	assert.Equal(t, 2.0, prometheustestutil.ToFloat64(writeBufferStatusUpdateErrors.WithLabelValues("get")))
	assert.Equal(t, 1.0, prometheustestutil.ToFloat64(writeBufferStatusUpdateErrors.WithLabelValues("marshal")))
	assert.Equal(t, 1.0, prometheustestutil.ToFloat64(writeBufferStatusUpdateErrors.WithLabelValues("patch")))
	// Untouched labels stay at zero.
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(writeBufferStatusUpdateErrors.WithLabelValues("other")))
}

func TestWriteBufferDepthGauge(t *testing.T) {
	prev := writeBufferLenFn.Load()
	t.Cleanup(func() {
		if prev == nil {
			writeBufferLenFn.Store(nil)
		} else {
			writeBufferLenFn.Store(prev)
		}
	})

	// Before installation the gauge reports 0.
	writeBufferLenFn.Store(nil)
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(writeBufferDepth))

	// After installation the gauge reflects the closure's return value live.
	depth := 0
	setWriteBufferLenSource(func() int { return depth })

	depth = 3
	assert.Equal(t, 3.0, prometheustestutil.ToFloat64(writeBufferDepth))

	depth = 0
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(writeBufferDepth))

	// A second call replaces the source.
	setWriteBufferLenSource(func() int { return 12 })
	assert.Equal(t, 12.0, prometheustestutil.ToFloat64(writeBufferDepth))
}

func TestInputRevisionBufferFlushErrorsLabels(t *testing.T) {
	inputRevisionBufferFlushErrors.Reset()

	inputRevisionBufferFlushErrors.WithLabelValues("get").Inc()
	inputRevisionBufferFlushErrors.WithLabelValues("get").Inc()
	inputRevisionBufferFlushErrors.WithLabelValues("patch").Inc()

	assert.Equal(t, 2.0, prometheustestutil.ToFloat64(inputRevisionBufferFlushErrors.WithLabelValues("get")))
	assert.Equal(t, 1.0, prometheustestutil.ToFloat64(inputRevisionBufferFlushErrors.WithLabelValues("patch")))
	// Untouched labels stay at zero.
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(inputRevisionBufferFlushErrors.WithLabelValues("other")))
}

func TestInputRevisionBufferDepthGauge(t *testing.T) {
	prev := inputRevisionBufferLenFn.Load()
	t.Cleanup(func() {
		if prev == nil {
			inputRevisionBufferLenFn.Store(nil)
		} else {
			inputRevisionBufferLenFn.Store(prev)
		}
	})

	// Before installation the gauge reports 0.
	inputRevisionBufferLenFn.Store(nil)
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(inputRevisionBufferDepth))

	// After installation the gauge reflects the closure's return value live.
	depth := 0
	setInputRevisionBufferLenSource(func() int { return depth })

	depth = 4
	assert.Equal(t, 4.0, prometheustestutil.ToFloat64(inputRevisionBufferDepth))

	depth = 0
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(inputRevisionBufferDepth))

	// A second call replaces the source.
	setInputRevisionBufferLenSource(func() int { return 7 })
	assert.Equal(t, 7.0, prometheustestutil.ToFloat64(inputRevisionBufferDepth))
}

