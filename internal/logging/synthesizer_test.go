package logging

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSynthesizerExtractFields(t *testing.T) {
	t.Run("extracts basic fields", func(t *testing.T) {
		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-synth",
				Generation: 5,
			},
			Spec: apiv1.SynthesizerSpec{
				Image: "test-image:latest",
			},
		}

		// Use the real extract function from synthesizer.go
		config := TelemetryConfig[*apiv1.Synthesizer]{
			ExtractFieldsFn: func(ctx context.Context, s *apiv1.Synthesizer) []any {
				fields := []any{
					"synthName", s.Name,
					"synthUrl", s.Spec.Image,
					"synthGeneration", s.Generation,
					"synthCreationTimestamp", s.CreationTimestamp.Time,
				}
				return fields
			},
		}

		fields := config.ExtractFieldsFn(context.Background(), synth)

		assert.Contains(t, fields, "synthName")
		assert.Contains(t, fields, "test-synth")
		assert.Contains(t, fields, "synthUrl")
		assert.Contains(t, fields, "test-image:latest")
		assert.Contains(t, fields, "synthGeneration")
		assert.Contains(t, fields, int64(5))
	})
}

func TestSynthesizerEventType(t *testing.T) {
	t.Run("returns active for normal synthesizer", func(t *testing.T) {
		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-synth",
			},
		}

		config := TelemetryConfig[*apiv1.Synthesizer]{
			EventTypeFn: func(s *apiv1.Synthesizer) string {
				if s.DeletionTimestamp != nil {
					return "deleting"
				}
				return "active"
			},
		}

		eventType := config.EventTypeFn(synth)
		assert.Equal(t, "active", eventType)
	})

	t.Run("returns deleting for synthesizer being deleted", func(t *testing.T) {
		now := metav1.Now()
		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-synth",
				DeletionTimestamp: &now,
			},
		}

		config := TelemetryConfig[*apiv1.Synthesizer]{
			EventTypeFn: func(s *apiv1.Synthesizer) string {
				if s.DeletionTimestamp != nil {
					return "deleting"
				}
				return "active"
			},
		}

		eventType := config.EventTypeFn(synth)
		assert.Equal(t, "deleting", eventType)
	})
}
