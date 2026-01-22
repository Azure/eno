package scheduling

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMissedReconciliation(t *testing.T) {
	tests := []struct {
		name     string
		comp     *apiv1.Composition
		expected bool
	}{
		{
			name: "No Current Synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: nil,
				},
			},
			expected: false,
		},
		{
			name: "Synthesis Reconciled",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Reconciled: &metav1.Time{Time: time.Now()},
					},
				},
			},
			expected: false,
		},
		{
			name: "Synthesis Not Initialized",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: nil,
					},
				},
			},
			expected: false,
		},
		{
			name: "Synthesis Missed Reconciliation",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: &metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
					},
				},
			},
			expected: true,
		},
		{
			name: "Synthesis Within Threshold",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: &metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
					},
				},
			},
			expected: false,
		},
		{
			name: "Composition Being Deleted",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: &metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
					},
				},
			},
			expected: false,
		},
		{
			name: "Composition Being Deleted",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
			},
			expected: false,
		},
		{
			name: "Composition Being Deleted, Missed",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-3 * time.Hour)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
				},
			},
			expected: true,
		},
		{
			name: "No Synthesis, Old Creation Timestamp",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: nil,
				},
			},
			expected: true,
		},
		{
			name: "No Synthesis, Old Creation Timestamp, Previous Synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
				},
				Status: apiv1.CompositionStatus{
					PreviousSynthesis: &apiv1.Synthesis{},
				},
			},
			expected: false,
		},
		{
			name: "No Synthesis, Recent Creation Timestamp",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: nil,
				},
			},
			expected: false,
		},
		{
			name: "Synthesis Not Initialized, Old Creation Timestamp",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: nil,
					},
				},
			},
			expected: true,
		},
		{
			name: "Synthesis Not Initialized, Recent Creation Timestamp",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: nil,
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := missedReconciliation(tt.comp, time.Hour)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCompositionHealthMetric(t *testing.T) {
	// Reset metric before test
	compositionHealth.Reset()

	tests := []struct {
		name          string
		comp          *apiv1.Composition
		expectedValue float64
	}{
		{
			name: "Healthy composition",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "healthy-comp",
					Namespace: "default",
				},
				Spec: apiv1.CompositionSpec{
					Synthesizer: apiv1.SynthesizerRef{Name: "test-synth"},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Reconciled: &metav1.Time{Time: time.Now()},
					},
				},
			},
			expectedValue: 0,
		},
		{
			name: "Stuck composition",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "stuck-comp",
					Namespace: "prod",
				},
				Spec: apiv1.CompositionSpec{
					Synthesizer: apiv1.SynthesizerRef{Name: "prod-synth"},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: &metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
					},
				},
			},
			expectedValue: 1,
		},
	}

	threshold := time.Hour
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate what the controller does: set metric based on missedReconciliation
			if missedReconciliation(tt.comp, threshold) {
				compositionHealth.WithLabelValues(tt.comp.Name, tt.comp.Namespace, tt.comp.Spec.Synthesizer.Name).Set(1)
			} else {
				compositionHealth.WithLabelValues(tt.comp.Name, tt.comp.Namespace, tt.comp.Spec.Synthesizer.Name).Set(0)
			}

			// Verify the metric value
			value := testutil.ToFloat64(compositionHealth.WithLabelValues(tt.comp.Name, tt.comp.Namespace, tt.comp.Spec.Synthesizer.Name))
			assert.Equal(t, tt.expectedValue, value, "composition %s/%s should have health value %v", tt.comp.Namespace, tt.comp.Name, tt.expectedValue)
		})
	}

	// Test that Reset() clears all metrics
	compositionHealth.Reset()
	// After reset, the metric should return 0 (default for non-existent gauge)
	value := testutil.ToFloat64(compositionHealth.WithLabelValues("healthy-comp", "default", "test-synth"))
	assert.Equal(t, float64(0), value, "metric should be 0 after reset")
}
