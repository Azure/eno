package reconciliation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSummarizeConditions(t *testing.T) {
	t.Run("nil when no status.conditions", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		assert.Nil(t, summarizeConditions(u))
	})

	t.Run("nil when status has no conditions", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{"replicas": int64(3)},
		}}
		assert.Nil(t, summarizeConditions(u))
	})

	t.Run("extracts type/status/reason/message", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":    "Available",
						"status":  "False",
						"reason":  "MinimumReplicasUnavailable",
						"message": "Deployment does not have minimum availability.",
					},
				},
			},
		}}

		got := summarizeConditions(u)
		assert.Equal(t, []map[string]string{
			{
				"type":    "Available",
				"status":  "False",
				"reason":  "MinimumReplicasUnavailable",
				"message": "Deployment does not have minimum availability.",
			},
		}, got)
	})

	t.Run("tolerates partial fields and skips non-map entries", func(t *testing.T) {
		u := &unstructured.Unstructured{Object: map[string]any{
			"status": map[string]any{
				"conditions": []any{
					map[string]any{"type": "Progressing", "status": "True"},
					"not-a-map",
				},
			},
		}}

		got := summarizeConditions(u)
		assert.Equal(t, []map[string]string{
			{"type": "Progressing", "status": "True", "reason": "", "message": ""},
		}, got)
	})
}

// TestSummarizeConditionsReasonStable guards the de-dup gate in logResourceNotReady: the
// reason string built from summarizeConditions must be identical across repeated calls on the
// same input, otherwise transition-only logging would spam on every readiness poll.
func TestSummarizeConditionsReasonStable(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Available", "status": "False", "reason": "MinReplicas", "message": "down"},
				map[string]any{"type": "Progressing", "status": "True", "reason": "NewRS", "message": "ok"},
			},
		},
	}}

	first := fmt.Sprintf("conditions=%v", summarizeConditions(u))
	for i := 0; i < 100; i++ {
		got := fmt.Sprintf("conditions=%v", summarizeConditions(u))
		assert.Equal(t, first, got, "reason string must be stable across calls")
	}
}
