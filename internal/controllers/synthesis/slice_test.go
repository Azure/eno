package synthesis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestSliceRecreation(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewSliceController(mgr.Manager))
	mgr.Start(t)

	// Create resource slice
	readyTime := metav1.Now()
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: &readyTime, Reconciled: true}}
	require.NoError(t, mgr.GetClient().Create(ctx, slice))
	require.NoError(t, mgr.GetClient().Status().Update(ctx, slice))

	// Create synthesizer
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	require.NoError(t, mgr.GetClient().Create(ctx, syn))

	// Create composition
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer = apiv1.SynthesizerRef{Name: syn.Name}
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	// Synthesis has completed with no error
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			Synthesized:                   ptr.To(metav1.Now()),
			ObservedCompositionGeneration: comp.Generation,
			ResourceSlices:                []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
		}
		return mgr.GetClient().Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Check resource slice is existed
	require.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Remove the finalizer
	slice.SetAnnotations(map[string]string{})
	require.NoError(t, mgr.GetClient().Update(ctx, slice))
	// Delete the resource slice
	require.NoError(t, mgr.GetClient().Delete(ctx, slice))
	// Check the resource slice is deleted
	testutil.Eventually(t, func() bool {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			return true
		}
		return false
	})
	s := &sliceController{client: mgr.GetClient()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err = s.Reconcile(ctx, req)
	require.NoError(t, err)

	// TODO: execute pod creation for re-synthesis process before checking the resource slice is recreated
	testutil.Eventually(t, func() bool {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if err != nil {
			return false
		}
		return true
	})
}

func TestNotEligibleForResynthesis(t *testing.T) {
	tests := []struct {
		name     string
		comp     *apiv1.Composition
		expected bool
	}{
		{
			name:     "CurrentSynthesis is nil",
			comp:     &apiv1.Composition{},
			expected: true,
		},
		{
			name: "CurrentSynthesis Synthesized is nil",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{}},
			},
			expected: true,
		},
		{
			name: "PendingResynthesis is not nil",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{PendingResynthesis: &metav1.Time{Time: time.Now()}},
			},
			expected: true,
		},
		{
			name: "composition deletion time stamp is not nil",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
			},
			expected: true,
		},
		{
			name: "composition deletion time stamp is not nil and PendingResynthesis is not nil",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: apiv1.CompositionStatus{PendingResynthesis: &metav1.Time{Time: time.Now()}},
			},
			expected: true,
		},
		{
			name: "CurrentSynthesis is nil and PendingResynthesis is not nil",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis:   nil,
					PendingResynthesis: &metav1.Time{Time: time.Now()},
				},
			},
			expected: true,
		},
		{
			name: "CurrentSynthesis Synthesized is nil and PendingResynthesis is not nil",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis:   &apiv1.Synthesis{Synthesized: nil},
					PendingResynthesis: &metav1.Time{Time: time.Now()},
				},
			},
			expected: true,
		},
		{
			name: "CurrentSynthesis is nil and deletion time stamp is not nil",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: apiv1.CompositionStatus{CurrentSynthesis: nil},
			},
			expected: true,
		},
		{
			name: "CurrentSynthesis Synthesized is nil and deletion time stamp is not nil",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{Synthesized: nil}},
			},
			expected: true,
		},
		{
			name: "composition is synthesized",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: &metav1.Time{Time: time.Now()},
					},
				},
			},
			expected: false,
		},
		{
			name: "composition is synthesized and PendingResynthesis is not nil",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: &metav1.Time{Time: time.Now()},
					},
					PendingResynthesis: &metav1.Time{Time: time.Now()},
				},
			},
			expected: true,
		},
		{
			name: "composition is synthesized and deletion time stamp is not nil",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: &metav1.Time{Time: time.Now()},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := notEligibleForResynthesis(tt.comp)
			assert.Equal(t, tt.expected, res)
		})
	}
}
