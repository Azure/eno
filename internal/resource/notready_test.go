package resource

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestObserveNotReadyReason(t *testing.T) {
	r := &Resource{}

	// First observation of any reason is a transition.
	assert.True(t, r.ObserveNotReadyReason("reason-a"), "first observation should report a change")

	// Re-observing the same reason is not a transition (suppresses log spam).
	assert.False(t, r.ObserveNotReadyReason("reason-a"), "identical reason should not report a change")
	assert.False(t, r.ObserveNotReadyReason("reason-a"))

	// A different reason is a transition again.
	assert.True(t, r.ObserveNotReadyReason("reason-b"), "changed reason should report a change")
	assert.False(t, r.ObserveNotReadyReason("reason-b"))

	// After reset, the next observation is a transition again even if the reason is unchanged.
	r.ResetNotReadyReason()
	assert.True(t, r.ObserveNotReadyReason("reason-b"), "observation after reset should report a change")
	assert.False(t, r.ObserveNotReadyReason("reason-b"))
}
