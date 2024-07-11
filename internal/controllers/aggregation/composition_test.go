package aggregation

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCompositionSimplification(t *testing.T) {
	tests := []struct {
		Bindings []apiv1.Binding
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
				Status: "WaitingForDispatch",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{UUID: "uuid"}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Synthesizing",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{UUID: "uuid", Synthesized: ptr.To(metav1.Now())}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Reconciling",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{UUID: "uuid", Reconciled: ptr.To(metav1.Now())}},
			Expected: apiv1.SimplifiedStatus{
				Status: "NotReady",
			},
		},
		{
			Input: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{UUID: "uuid", Ready: ptr.To(metav1.Now())}},
			Expected: apiv1.SimplifiedStatus{
				Status: "Ready",
			},
		},
		{
			Bindings: []apiv1.Binding{{Key: "foo"}},
			Input:    apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{UUID: "uuid"}},
			Expected: apiv1.SimplifiedStatus{
				Status: "MissingInputs",
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
					UUID:    "uuid",
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
					UUID:  "uuid",
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
			comp.Spec.Bindings = tc.Bindings
			if tc.Deleting {
				comp.DeletionTimestamp = ptr.To(metav1.Now())
			}
			output := c.aggregate(comp)
			assert.Equal(t, tc.Expected, *output)
		})
	}
}

func TestCompositionSimplificationI(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewCompositionController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return comp.Status.Simplified != nil && comp.Status.Simplified.Status != ""
	})
}
