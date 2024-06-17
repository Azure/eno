package aggregation

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestCompositionSimplification(t *testing.T) {
	tests := []struct {
		Input    apiv1.CompositionStatus
		Deleting bool
		Expected apiv1.SimplifiedStatus
	}{
		{
			Input: apiv1.CompositionStatus{},
			Expected: apiv1.SimplifiedStatus{
				Status: "PendingSynthesis",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Synthesizing",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{Synthesized: ptr.To(metav1.Now())}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Reconciling",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{Reconciled: ptr.To(metav1.Now())}},
			Expected: apiv1.SimplifiedStatus{
				Status: "NotReady",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{Ready: ptr.To(metav1.Now())}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Ready",
			},
		},
		{
			Deleting: true,
			Expected: apiv1.SimplifiedStatus{
				Status: "Deleting",
			},
		},
		{
			Input: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					Ready:   ptr.To(metav1.Now()),
					Results: []apiv1.Result{{Message: "foo", Severity: "error"}},
				}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Ready",
				Error:  "foo",
			},
		},
		{
			Input: apiv1.CompositionStatus{
				PendingResynthesis: ptr.To(metav1.Now()),
				CurrentSynthesis: &apiv1.Synthesis{
					Ready: ptr.To(metav1.Now()),
				}},
			Expected: apiv1.SimplifiedStatus{
				Status: "WaitingForCooldown",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Expected.Status, func(t *testing.T) {
			c := &compositionController{}
			comp := &apiv1.Composition{Status: tc.Input}
			if tc.Deleting {
				comp.DeletionTimestamp = ptr.To(metav1.Now())
			}
			output := c.aggregate(comp)
			assert.Equal(t, tc.Expected, *output)
		})
	}
}
