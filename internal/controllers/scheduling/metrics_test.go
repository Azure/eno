package scheduling

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
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

func TestCompositionStatusMetric(t *testing.T) {
	tests := []struct {
		name           string
		comp           *apiv1.Composition
		expectedStatus float64
	}{
		{
			name: "Healthy composition - reconciled",
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
			expectedStatus: 0,
		},
		{
			name: "Stuck composition - missed reconciliation",
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
			expectedStatus: 1,
		},
		{
			name: "Healthy composition - within threshold",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "recent-comp",
					Namespace: "staging",
				},
				Spec: apiv1.CompositionSpec{
					Synthesizer: apiv1.SynthesizerRef{Name: "staging-synth"},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Initialized: &metav1.Time{Time: time.Now().Add(-30 * time.Minute)},
					},
				},
			},
			expectedStatus: 0,
		},
		{
			name: "Stuck composition - stuck deleting",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "deleting-comp",
					Namespace:         "default",
					DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-3 * time.Hour)},
				},
				Spec: apiv1.CompositionSpec{
					Synthesizer: apiv1.SynthesizerRef{Name: "test-synth"},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
				},
			},
			expectedStatus: 1,
		},
		{
			name: "Stuck composition - no synthesis, old creation",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "old-comp",
					Namespace:         "default",
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-2 * time.Hour)},
				},
				Spec: apiv1.CompositionSpec{
					Synthesizer: apiv1.SynthesizerRef{Name: "test-synth"},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: nil,
				},
			},
			expectedStatus: 1,
		},
	}

	threshold := time.Hour
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isStuck := missedReconciliation(tt.comp, threshold)
			var status float64
			if isStuck {
				status = 1
			} else {
				status = 0
			}
			assert.Equal(t, tt.expectedStatus, status, "composition %s/%s with synthesizer %s should have status %v",
				tt.comp.Namespace, tt.comp.Name, tt.comp.Spec.Synthesizer.Name, tt.expectedStatus)
		})
	}
}
