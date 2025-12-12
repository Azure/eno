package logging

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestCompositionStatusLoggerPredicate(t *testing.T) {
	predicate := compositionPredicate()

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

func TestExtractCompositionFields(t *testing.T) {
	t.Run("extract fields with full status", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "test-comp"
		comp.Namespace = "default"
		comp.Generation = 1
		comp.Status = apiv1.CompositionStatus{
			Simplified: &apiv1.SimplifiedStatus{
				Status: "Ready",
				Error:  "",
			},
			CurrentSynthesis: &apiv1.Synthesis{
				UUID:                          "test-uuid-123",
				ObservedSynthesizerGeneration: 5,
			},
		}

		fields := extractCompositionFields(context.TODO(), comp)

		assert.Contains(t, fields, "compositionName")
		assert.Contains(t, fields, "test-comp")
		assert.Contains(t, fields, "compositionNamespace")
		assert.Contains(t, fields, "default")
		assert.Contains(t, fields, "compositionGeneration")
		assert.Contains(t, fields, int64(1))
		assert.Contains(t, fields, "currentSynthesisUUID")
		assert.Contains(t, fields, "test-uuid-123")
		assert.Contains(t, fields, "currentSynthesizerGeneration")
		assert.Contains(t, fields, int64(5))
		assert.Contains(t, fields, "status")
		assert.Contains(t, fields, "Ready")
		assert.Contains(t, fields, "error")
		assert.Contains(t, fields, "")
	})

	t.Run("extract fields with inflight synthesis", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "test-comp-inflight"
		comp.Namespace = "default"
		comp.Generation = 2
		comp.Status = apiv1.CompositionStatus{
			Simplified: &apiv1.SimplifiedStatus{
				Status: "Synthesizing",
				Error:  "",
			},
			InFlightSynthesis: &apiv1.Synthesis{
				UUID:                          "inflight-uuid-456",
				ObservedSynthesizerGeneration: 7,
			},
		}

		fields := extractCompositionFields(context.TODO(), comp)

		assert.Contains(t, fields, "compositionName")
		assert.Contains(t, fields, "test-comp-inflight")
		assert.Contains(t, fields, "compositionNamespace")
		assert.Contains(t, fields, "default")
		assert.Contains(t, fields, "compositionGeneration")
		assert.Contains(t, fields, int64(2))
		assert.Contains(t, fields, "inflightSynthesisUUID")
		assert.Contains(t, fields, "inflight-uuid-456")
		assert.Contains(t, fields, "inflightSynthesizerGeneration")
		assert.Contains(t, fields, int64(7))
		assert.Contains(t, fields, "status")
		assert.Contains(t, fields, "Synthesizing")
	})

	t.Run("extract fields with both current and inflight synthesis", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "test-comp-both"
		comp.Namespace = "default"
		comp.Generation = 3
		comp.Status = apiv1.CompositionStatus{
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
		}

		fields := extractCompositionFields(context.TODO(), comp)

		// Check current synthesis fields
		assert.Contains(t, fields, "currentSynthesisUUID")
		assert.Contains(t, fields, "current-uuid-789")
		assert.Contains(t, fields, "currentSynthesizerGeneration")
		assert.Contains(t, fields, int64(8))

		// Check inflight synthesis fields
		assert.Contains(t, fields, "inflightSynthesisUUID")
		assert.Contains(t, fields, "inflight-uuid-012")
		assert.Contains(t, fields, "inflightSynthesizerGeneration")
		assert.Contains(t, fields, int64(9))
	})

	t.Run("extract fields with nil simplified status", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "test-comp"
		comp.Namespace = "default"
		comp.Generation = 1
		comp.Status = apiv1.CompositionStatus{
			Simplified: nil,
		}

		fields := extractCompositionFields(context.TODO(), comp)

		assert.Contains(t, fields, "compositionName")
		assert.Contains(t, fields, "test-comp")
		assert.Contains(t, fields, "compositionNamespace")
		assert.Contains(t, fields, "default")
		assert.Contains(t, fields, "compositionGeneration")
		assert.Contains(t, fields, int64(1))
		// Should not contain status fields
		assert.NotContains(t, fields, "status")
	})

	t.Run("extract fields with error status", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Name = "test-comp"
		comp.Namespace = "default"
		comp.Generation = 2
		comp.Status = apiv1.CompositionStatus{
			Simplified: &apiv1.SimplifiedStatus{
				Status: "NotReady",
				Error:  "synthesis failed",
			},
		}

		fields := extractCompositionFields(context.TODO(), comp)

		assert.Contains(t, fields, "status")
		assert.Contains(t, fields, "NotReady")
		assert.Contains(t, fields, "error")
		assert.Contains(t, fields, "synthesis failed")
	})
}

func TestCompositionEventType(t *testing.T) {
	t.Run("event type with simplified status", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Status = apiv1.CompositionStatus{
			Simplified: &apiv1.SimplifiedStatus{
				Status: "Ready",
			},
		}

		eventType := compositionEventType(comp)
		assert.Equal(t, "status_update", eventType)
	})

	t.Run("event type with nil simplified status", func(t *testing.T) {
		comp := &apiv1.Composition{}
		comp.Status = apiv1.CompositionStatus{
			Simplified: nil,
		}

		eventType := compositionEventType(comp)
		assert.Equal(t, "status_created", eventType)
	})

	t.Run("event type with deletion timestamp", func(t *testing.T) {
		comp := &apiv1.Composition{}
		now := metav1.Now()
		comp.DeletionTimestamp = &now
		comp.Status = apiv1.CompositionStatus{
			Simplified: &apiv1.SimplifiedStatus{
				Status: "Ready",
			},
		}

		eventType := compositionEventType(comp)
		assert.Equal(t, "status_deleting", eventType)
	})
}
