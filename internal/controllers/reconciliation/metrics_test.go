package reconciliation

import (
	"testing"

	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestReconciliationsInFlightGauge(t *testing.T) {
	reconciliationsInFlight.Set(0)

	reconciliationsInFlight.Inc()
	reconciliationsInFlight.Inc()
	assert.Equal(t, 2.0, prometheustestutil.ToFloat64(reconciliationsInFlight))

	reconciliationsInFlight.Dec()
	assert.Equal(t, 1.0, prometheustestutil.ToFloat64(reconciliationsInFlight))

	reconciliationsInFlight.Dec()
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(reconciliationsInFlight))
}

func TestReconciliationMaxConcurrentGauge(t *testing.T) {
	reconciliationMaxConcurrent.Set(0)

	reconciliationMaxConcurrent.Set(42)
	assert.Equal(t, 42.0, prometheustestutil.ToFloat64(reconciliationMaxConcurrent))

	// Subsequent Set replaces, not accumulates.
	reconciliationMaxConcurrent.Set(7)
	assert.Equal(t, 7.0, prometheustestutil.ToFloat64(reconciliationMaxConcurrent))
}

func TestReconciliationWorkqueueDepthGauge(t *testing.T) {
	// Restore any previously installed source after the test.
	prev := workqueueLenFn.Load()
	t.Cleanup(func() {
		if prev == nil {
			workqueueLenFn.Store(nil)
		} else {
			workqueueLenFn.Store(prev)
		}
	})

	// Before any source is installed the gauge reports 0.
	workqueueLenFn.Store(nil)
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(reconciliationWorkqueueDepth))

	// After installation the gauge reflects the closure's return value live.
	depth := 0
	setWorkqueueLenSource(func() int { return depth })

	depth = 5
	assert.Equal(t, 5.0, prometheustestutil.ToFloat64(reconciliationWorkqueueDepth))

	depth = 0
	assert.Equal(t, 0.0, prometheustestutil.ToFloat64(reconciliationWorkqueueDepth))

	// A second call replaces the source (last-writer-wins).
	setWorkqueueLenSource(func() int { return 99 })
	assert.Equal(t, 99.0, prometheustestutil.ToFloat64(reconciliationWorkqueueDepth))
}
