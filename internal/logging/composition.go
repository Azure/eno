package logging

import (
	"context"
	"reflect"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	ctrl "sigs.k8s.io/controller-runtime"
)

// NewCompositionStatusLogger creates a telemetry controller for logging Composition status changes
func NewCompositionStatusLogger(mgr ctrl.Manager, freq time.Duration) error {
	return NewTelemetryController(
		TelemetryConfig[*apiv1.Composition]{
			Manager:         mgr,
			Frequency:       freq,
			PredicateFn:     compositionPredicate,
			ExtractFieldsFn: extractCompositionFields,
			EventTypeFn:     compositionEventType,
			MessageFn:       func() string { return "current composition status" },
			ControllerName:  "compositionStatusLogger",
			Logger:          NewLogger(),
		},
		&apiv1.Composition{},
	)
}

// compositionPredicate defines when to log composition status changes
func compositionPredicate() predicate.Predicate {
	return &predicate.Funcs{
		CreateFunc:  func(tce event.TypedCreateEvent[client.Object]) bool { return true },
		DeleteFunc:  func(tde event.TypedDeleteEvent[client.Object]) bool { return true },
		GenericFunc: func(tge event.TypedGenericEvent[client.Object]) bool { return false },
		UpdateFunc: func(tue event.TypedUpdateEvent[client.Object]) bool {
			compA, okA := tue.ObjectNew.(*apiv1.Composition)
			compB, okB := tue.ObjectOld.(*apiv1.Composition)
			return okA && okB && !reflect.DeepEqual(compA.Status.Simplified, compB.Status.Simplified)
		},
	}
}

// extractCompositionFields extracts relevant fields from a Composition for logging
func extractCompositionFields(ctx context.Context, comp *apiv1.Composition) []any {
	if comp.Status.Simplified == nil {
		return []any{
			"compositionName", comp.Name,
			"compositionNamespace", comp.Namespace,
			"compositionGeneration", comp.Generation,
		}
	}

	fields := []any{
		"compositionName", comp.Name,
		"compositionNamespace", comp.Namespace,
		"compositionGeneration", comp.Generation,
		"status", comp.Status.Simplified.Status,
		"error", comp.Status.Simplified.Error,
	}

	if syn := comp.Status.CurrentSynthesis; syn != nil {
		fields = append(fields,
			"currentSynthesisUUID", syn.UUID,
			"currentSynthesizerGeneration", syn.ObservedSynthesizerGeneration,
		)
	}

	if syn := comp.Status.InFlightSynthesis; syn != nil {
		fields = append(fields,
			"inflightSynthesisUUID", syn.UUID,
			"inflightSynthesizerGeneration", syn.ObservedSynthesizerGeneration,
		)
	}

	return fields
}

// compositionEventType determines the event type for composition logging
func compositionEventType(comp *apiv1.Composition) string {
	if comp.Status.Simplified == nil {
		return "status_update"
	}
	return "status_update"
}
