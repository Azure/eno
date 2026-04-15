package logging

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLogger(t *testing.T) {
	logger := NewLogger()
	require.NotNil(t, logger)
	require.NotNil(t, logger.logFn)
}

func TestLogger_Log(t *testing.T) {
	t.Run("logs with timestamp", func(t *testing.T) {
		var capturedMsg string
		var capturedArgs []any

		logger := NewLogger()
		logger = logger.WithLogFn(func(ctx context.Context, msg string, args ...any) {
			capturedMsg = msg
			capturedArgs = args
		})

		ctx := context.Background()
		logger.Log(ctx, "test message", "key1", "value1", "key2", 42)

		assert.Equal(t, "test message", capturedMsg)
		require.Len(t, capturedArgs, 6) // timestamp + 2 key-value pairs = 6 items

		// Check timestamp is first
		assert.Equal(t, "timestamp", capturedArgs[0])
		_, ok := capturedArgs[1].(time.Time)
		assert.True(t, ok, "second arg should be a time.Time")

		// Check user fields are included
		assert.Equal(t, "key1", capturedArgs[2])
		assert.Equal(t, "value1", capturedArgs[3])
		assert.Equal(t, "key2", capturedArgs[4])
		assert.Equal(t, 42, capturedArgs[5])
	})

	t.Run("handles empty fields", func(t *testing.T) {
		var capturedArgs []any

		logger := NewLogger()
		logger = logger.WithLogFn(func(ctx context.Context, msg string, args ...any) {
			capturedArgs = args
		})

		ctx := context.Background()
		logger.Log(ctx, "empty fields")

		require.Len(t, capturedArgs, 2) // only timestamp
		assert.Equal(t, "timestamp", capturedArgs[0])
	})

	t.Run("thread safety", func(t *testing.T) {
		var mu sync.Mutex
		logCount := 0

		logger := NewLogger()
		logger = logger.WithLogFn(func(ctx context.Context, msg string, args ...any) {
			mu.Lock()
			logCount++
			mu.Unlock()
		})

		ctx := context.Background()
		var wg sync.WaitGroup

		// Launch 100 concurrent log calls
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				logger.Log(ctx, "concurrent log", "iteration", n)
			}(i)
		}

		wg.Wait()
		assert.Equal(t, 100, logCount)
	})
}

func TestLogger_WithLogFn(t *testing.T) {
	t.Run("replaces log function", func(t *testing.T) {
		called := false
		customFn := func(ctx context.Context, msg string, args ...any) {
			called = true
		}

		logger := NewLogger()
		logger = logger.WithLogFn(customFn)

		logger.Log(context.Background(), "test")
		assert.True(t, called)
	})

	t.Run("returns same logger instance", func(t *testing.T) {
		logger := NewLogger()
		modified := logger.WithLogFn(func(ctx context.Context, msg string, args ...any) {})

		assert.Equal(t, logger, modified)
	})
}

func TestAddFields(t *testing.T) {
	t.Run("adds fields to empty base", func(t *testing.T) {
		base := []any{}
		result := AddFields(base, "key1", "value1", "key2", "value2")

		assert.Equal(t, []any{"key1", "value1", "key2", "value2"}, result)
	})

	t.Run("appends to existing fields", func(t *testing.T) {
		base := []any{"existing", "field"}
		result := AddFields(base, "new", "field")

		assert.Equal(t, []any{"existing", "field", "new", "field"}, result)
	})

	t.Run("handles empty addition", func(t *testing.T) {
		base := []any{"key", "value"}
		result := AddFields(base)

		assert.Equal(t, []any{"key", "value"}, result)
	})

	t.Run("preserves original base slice", func(t *testing.T) {
		base := []any{"key", "value"}
		result := AddFields(base, "new", "field")

		// Original should be unchanged
		assert.Equal(t, []any{"key", "value"}, base)
		// Result should have new fields
		assert.Equal(t, []any{"key", "value", "new", "field"}, result)
	})
}
