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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := missedReconciliation(tt.comp, time.Hour)
			assert.Equal(t, tt.expected, result)
		})
	}
}
