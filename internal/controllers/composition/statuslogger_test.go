package composition

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestStatusLoggerPredicate(t *testing.T) {
	logger := &statusLogger{}
	predicate := logger.newPredicate()

	t.Run("CreateFunc returns true", func(t *testing.T) {
		comp := &apiv1.Composition{}
		event := event.TypedCreateEvent[client.Object]{Object: comp}
		assert.True(t, predicate.Create(event))
	})

	t.Run("DeleteFunc returns true", func(t *testing.T) {
		comp := &apiv1.Composition{}
		event := event.TypedDeleteEvent[client.Object]{Object: comp}
		assert.True(t, predicate.Delete(event))
	})

	t.Run("GenericFunc returns false", func(t *testing.T) {
		comp := &apiv1.Composition{}
		event := event.TypedGenericEvent[client.Object]{Object: comp}
		assert.False(t, predicate.Generic(event))
	})

	t.Run("UpdateFunc with same simplified status returns false", func(t *testing.T) {
		simplifiedStatus := &apiv1.SimplifiedStatus{Status: "Ready", Error: ""}

		compA := &apiv1.Composition{
			Status: apiv1.CompositionStatus{Simplified: simplifiedStatus},
		}
		compB := &apiv1.Composition{
			Status: apiv1.CompositionStatus{Simplified: simplifiedStatus},
		}

		event := event.TypedUpdateEvent[client.Object]{
			ObjectNew: compA,
			ObjectOld: compB,
		}
		assert.False(t, predicate.Update(event))
	})

	t.Run("UpdateFunc with different simplified status returns true", func(t *testing.T) {
		compA := &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				Simplified: &apiv1.SimplifiedStatus{Status: "Ready", Error: ""},
			},
		}
		compB := &apiv1.Composition{
			Status: apiv1.CompositionStatus{
				Simplified: &apiv1.SimplifiedStatus{Status: "Synthesizing", Error: ""},
			},
		}

		event := event.TypedUpdateEvent[client.Object]{
			ObjectNew: compA,
			ObjectOld: compB,
		}
		assert.True(t, predicate.Update(event))
	})

	t.Run("UpdateFunc with nil simplified status returns false", func(t *testing.T) {
		compA := &apiv1.Composition{}
		compB := &apiv1.Composition{}

		event := event.TypedUpdateEvent[client.Object]{
			ObjectNew: compA,
			ObjectOld: compB,
		}
		assert.False(t, predicate.Update(event))
	})

	t.Run("UpdateFunc with non-Composition objects returns false", func(t *testing.T) {
		objectA := &corev1.ConfigMap{}
		objectB := &corev1.ConfigMap{}

		event := event.TypedUpdateEvent[client.Object]{
			ObjectNew: objectA,
			ObjectOld: objectB,
		}
		assert.False(t, predicate.Update(event))
	})
}

func TestStatusLoggerReconcile(t *testing.T) {
	ctx := testutil.NewContext(t)
	frequency := 30 * time.Second

	t.Run("successful reconcile with existing composition", func(t *testing.T) {
		comp := &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-comp",
				Namespace:  "default",
				Generation: 1,
			},
			Status: apiv1.CompositionStatus{
				Simplified: &apiv1.SimplifiedStatus{
					Status: "Ready",
					Error:  "",
				},
				CurrentSynthesis: &apiv1.Synthesis{
					UUID:                          "test-uuid-123",
					ObservedSynthesizerGeneration: 5,
				},
			},
		}

		cli := testutil.NewClient(t, comp)

		var loggedMsg string
		var loggedArgs []any
		logger := &statusLogger{
			client:    cli,
			frequency: frequency,
			logFn: func(ctx context.Context, msg string, args ...any) {
				loggedMsg = msg
				loggedArgs = args
			},
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-comp",
				Namespace: "default",
			},
		}

		result, err := logger.Reconcile(ctx, req)
		require.NoError(t, err)

		assert.Equal(t, "current composition status", loggedMsg)
		assert.Contains(t, loggedArgs, "compositionName")
		assert.Contains(t, loggedArgs, "test-comp")
		assert.Contains(t, loggedArgs, "compositionNamespace")
		assert.Contains(t, loggedArgs, "default")
		assert.Contains(t, loggedArgs, "compositionGeneration")
		assert.Contains(t, loggedArgs, int64(1))
		assert.Contains(t, loggedArgs, "currentSynthesisUUID")
		assert.Contains(t, loggedArgs, "test-uuid-123")
		assert.Contains(t, loggedArgs, "currentSynthesizerGeneration")
		assert.Contains(t, loggedArgs, int64(5))
		assert.Contains(t, loggedArgs, "status")
		assert.Contains(t, loggedArgs, "Ready")
		assert.Contains(t, loggedArgs, "error")
		assert.Contains(t, loggedArgs, "")

		assert.True(t, result.RequeueAfter >= frequency*8/10)  // at least 80% of frequency due to jitter
		assert.True(t, result.RequeueAfter <= frequency*12/10) // at most 120% of frequency due to jitter
	})

	t.Run("successful reconcile with inflight synthesis", func(t *testing.T) {
		comp := &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-comp-inflight",
				Namespace:  "default",
				Generation: 2,
			},
			Status: apiv1.CompositionStatus{
				Simplified: &apiv1.SimplifiedStatus{
					Status: "Synthesizing",
					Error:  "",
				},
				InFlightSynthesis: &apiv1.Synthesis{
					UUID:                          "inflight-uuid-456",
					ObservedSynthesizerGeneration: 7,
				},
			},
		}

		cli := testutil.NewClient(t, comp)

		var loggedMsg string
		var loggedArgs []any
		logger := &statusLogger{
			client:    cli,
			frequency: frequency,
			logFn: func(ctx context.Context, msg string, args ...any) {
				loggedMsg = msg
				loggedArgs = args
			},
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-comp-inflight",
				Namespace: "default",
			},
		}

		result, err := logger.Reconcile(ctx, req)
		require.NoError(t, err)

		assert.Equal(t, "current composition status", loggedMsg)
		assert.Contains(t, loggedArgs, "compositionName")
		assert.Contains(t, loggedArgs, "test-comp-inflight")
		assert.Contains(t, loggedArgs, "compositionNamespace")
		assert.Contains(t, loggedArgs, "default")
		assert.Contains(t, loggedArgs, "compositionGeneration")
		assert.Contains(t, loggedArgs, int64(2))
		assert.Contains(t, loggedArgs, "inflightSynthesisUUID")
		assert.Contains(t, loggedArgs, "inflight-uuid-456")
		assert.Contains(t, loggedArgs, "inflightSynthesizerGeneration")
		assert.Contains(t, loggedArgs, int64(7))
		assert.Contains(t, loggedArgs, "status")
		assert.Contains(t, loggedArgs, "Synthesizing")
		assert.Contains(t, loggedArgs, "error")
		assert.Contains(t, loggedArgs, "")

		assert.True(t, result.RequeueAfter >= frequency*8/10)  // at least 80% of frequency due to jitter
		assert.True(t, result.RequeueAfter <= frequency*12/10) // at most 120% of frequency due to jitter
	})

	t.Run("successful reconcile with both current and inflight synthesis", func(t *testing.T) {
		comp := &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-comp-both",
				Namespace:  "default",
				Generation: 3,
			},
			Status: apiv1.CompositionStatus{
				Simplified: &apiv1.SimplifiedStatus{
					Status: "Synthesizing",
					Error:  "",
				},
				CurrentSynthesis: &apiv1.Synthesis{
					UUID:                          "current-uuid-789",
					ObservedSynthesizerGeneration: 8,
				},
				InFlightSynthesis: &apiv1.Synthesis{
					UUID:                          "inflight-uuid-012",
					ObservedSynthesizerGeneration: 9,
				},
			},
		}

		cli := testutil.NewClient(t, comp)

		var loggedArgs []any
		logger := &statusLogger{
			client:    cli,
			frequency: frequency,
			logFn: func(ctx context.Context, msg string, args ...any) {
				loggedArgs = args
			},
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-comp-both",
				Namespace: "default",
			},
		}

		result, err := logger.Reconcile(ctx, req)
		require.NoError(t, err)

		// Check current synthesis fields
		assert.Contains(t, loggedArgs, "currentSynthesisUUID")
		assert.Contains(t, loggedArgs, "current-uuid-789")
		assert.Contains(t, loggedArgs, "currentSynthesizerGeneration")
		assert.Contains(t, loggedArgs, int64(8))

		// Check inflight synthesis fields
		assert.Contains(t, loggedArgs, "inflightSynthesisUUID")
		assert.Contains(t, loggedArgs, "inflight-uuid-012")
		assert.Contains(t, loggedArgs, "inflightSynthesizerGeneration")
		assert.Contains(t, loggedArgs, int64(9))

		assert.True(t, result.RequeueAfter > 0)
	})

	t.Run("reconcile with composition not found", func(t *testing.T) {
		cli := testutil.NewClient(t)

		var logCalled bool
		logger := &statusLogger{
			client:    cli,
			frequency: frequency,
			logFn: func(ctx context.Context, msg string, args ...any) {
				logCalled = true
			},
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "non-existent-comp",
				Namespace: "default",
			},
		}

		result, err := logger.Reconcile(ctx, req)
		require.Error(t, err)
		assert.False(t, logCalled)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("reconcile with nil simplified status", func(t *testing.T) {
		comp := &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-comp",
				Namespace: "default",
			},
			Status: apiv1.CompositionStatus{
				Simplified: nil,
			},
		}

		cli := testutil.NewClient(t, comp)

		var logCalled bool
		logger := &statusLogger{
			client:    cli,
			frequency: frequency,
			logFn: func(ctx context.Context, msg string, args ...any) {
				logCalled = true
			},
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-comp",
				Namespace: "default",
			},
		}

		result, err := logger.Reconcile(ctx, req)
		require.NoError(t, err)
		assert.False(t, logCalled)
		assert.Equal(t, reconcile.Result{}, result)
	})

	t.Run("reconcile with error status", func(t *testing.T) {
		comp := &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-comp",
				Namespace:  "default",
				Generation: 2,
			},
			Status: apiv1.CompositionStatus{
				Simplified: &apiv1.SimplifiedStatus{
					Status: "NotReady",
					Error:  "synthesis failed",
				},
			},
		}

		cli := testutil.NewClient(t, comp)

		var loggedArgs []any
		logger := &statusLogger{
			client:    cli,
			frequency: frequency,
			logFn: func(ctx context.Context, msg string, args ...any) {
				loggedArgs = args
			},
		}

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      "test-comp",
				Namespace: "default",
			},
		}

		result, err := logger.Reconcile(ctx, req)
		require.NoError(t, err)

		assert.Contains(t, loggedArgs, "status")
		assert.Contains(t, loggedArgs, "NotReady")
		assert.Contains(t, loggedArgs, "error")
		assert.Contains(t, loggedArgs, "synthesis failed")
		assert.True(t, result.RequeueAfter > 0)
	})
}
