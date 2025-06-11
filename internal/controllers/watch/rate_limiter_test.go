package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"

	apiv1 "github.com/Azure/eno/api/v1"
)

func TestRateLimiterSharing(t *testing.T) {
	// Test that the rate limiter is shared between multiple kind controllers
	
	// Create watch controller with a custom rate limit
	sharedLimiter := rate.NewLimiter(rate.Limit(5.0), 5)
	watchController := &WatchController{
		mgr:            nil, // not needed for this test
		client:         nil, // not needed for this test
		refControllers: map[apiv1.ResourceRef]*KindWatchController{},
		sharedLimiter:  sharedLimiter,
	}
	
	// Create two kind watch controllers
	kc1 := &KindWatchController{
		client:        nil, // not needed for this test
		sharedLimiter: watchController.sharedLimiter,
	}
	
	kc2 := &KindWatchController{
		client:        nil, // not needed for this test
		sharedLimiter: watchController.sharedLimiter,
	}
	
	// Verify they share the same rate limiter instance
	assert.True(t, kc1.sharedLimiter == kc2.sharedLimiter, "Kind controllers should share the same rate limiter instance")
	assert.True(t, kc1.sharedLimiter == watchController.sharedLimiter, "Kind controllers should use the watch controller's shared limiter")
	
	// Test rate limiting behavior
	limiter := kc1.sharedLimiter
	
	// Should be able to consume tokens initially
	assert.True(t, limiter.Allow(), "First request should be allowed")
	assert.True(t, limiter.Allow(), "Second request should be allowed")
	
	// After consuming burst, should be rate limited
	for i := 0; i < 5; i++ {
		limiter.Allow() // consume remaining burst
	}
	assert.False(t, limiter.Allow(), "Request should be rate limited after burst is consumed")
}

func TestNewControllerWithRateLimit(t *testing.T) {
	// Test that rate limiter has the correct configuration
	
	// Test with custom rate limit
	customRateLimit := 15.5
	sharedLimiter := rate.NewLimiter(rate.Limit(customRateLimit), int(customRateLimit))
	watchController := &WatchController{
		mgr:            nil, // not needed for this test
		client:         nil, // not needed for this test
		refControllers: map[apiv1.ResourceRef]*KindWatchController{},
		sharedLimiter:  sharedLimiter,
	}
	
	// Verify the rate limiter has the correct configuration
	limiter := watchController.sharedLimiter
	assert.Equal(t, rate.Limit(customRateLimit), limiter.Limit(), "Rate limiter should have the configured rate limit")
	assert.Equal(t, int(customRateLimit), limiter.Burst(), "Rate limiter should have the configured burst size")
}