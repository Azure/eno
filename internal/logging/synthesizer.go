package logging

import (
	"context"
	"reflect"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NewSynthesizerTelemetry creates a telemetry controller for Synthesizer CRs
func NewSynthesizerTelemetryLogger(mgr ctrl.Manager, freq time.Duration) error {
	config := TelemetryConfig[*apiv1.Synthesizer]{
		Manager:        mgr,
		Frequency:      freq,
		ControllerName: "synthesizerTelemetryController",
		MessageFn:      func() string { return "synthesizer telemetry event" },

		PredicateFn: func() predicate.Predicate {
			return &predicate.Funcs{
				CreateFunc:  func(tce event.TypedCreateEvent[client.Object]) bool { return true },
				DeleteFunc:  func(tde event.TypedDeleteEvent[client.Object]) bool { return true },
				GenericFunc: func(tge event.TypedGenericEvent[client.Object]) bool { return false },
				UpdateFunc: func(tue event.TypedUpdateEvent[client.Object]) bool {
					synthA, okA := tue.ObjectNew.(*apiv1.Synthesizer)
					synthB, okB := tue.ObjectOld.(*apiv1.Synthesizer)
					if !okA || !okB {
						return false
					}
					return !reflect.DeepEqual(synthA.Spec, synthB.Spec) || synthA.Generation != synthB.Generation
				},
			}
		},

		ExtractFieldsFn: func(ctx context.Context, synth *apiv1.Synthesizer) []any {
			fields := []any{
				"synthName", synth.Name,
				"synthUrl", synth.Spec.Image,
				"synthGeneration", synth.Generation,
				"synthCreationTimestamp", synth.CreationTimestamp.Time,
			}
			return fields
		},

		EventTypeFn: func(synth *apiv1.Synthesizer) string {
			if synth.DeletionTimestamp != nil {
				return "status_deleting"
			}
			return "status_created"
		},
	}
	return NewTelemetryController(config, &apiv1.Synthesizer{})
}
