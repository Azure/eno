package synthesis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

// TestSliceCleanupControllerOrphanedSlice proves that slices owned by a composition that
// does not reference them will eventually be GC'd.
func TestSliceCleanupControllerOrphanedSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewSliceCleanupController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	// Synthesis has completed with no resulting resource slices
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			Synthesized:                   ptr.To(metav1.Now()),
			ObservedCompositionGeneration: comp.Generation,
		}
		return mgr.GetClient().Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// A random slice is created, but not part of the composition's synthesis
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = "default"
	slice.Finalizers = []string{"eno.azure.io/cleanup"}
	slice.Spec.CompositionGeneration = comp.Generation - 1 // it's out of date
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, mgr.GetClient().Create(ctx, slice))

	// Slice should eventually be deleted
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice))
	})
}

func TestShouldDeleteSlice(t *testing.T) {
	tests := []struct {
		name     string
		comp     *apiv1.Composition
		slice    *apiv1.ResourceSlice
		expected bool
	}{
		{
			name: "stale informer (CurrentSynthesis is nil)",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: nil,
				},
			},
			slice: &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					CompositionGeneration: 2,
				},
			},
			expected: false,
		},
		{
			name: "stale informer (synthesis is stale)",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{ObservedCompositionGeneration: 1},
				},
			},
			slice: &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					CompositionGeneration: 2,
				},
			},
			expected: false,
		},
		{
			name: "another attempt started for a different synthesis, old one still references the slice",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Attempts: 5,
						UUID:     "the-next-one",
					},
					PreviousSynthesis: &apiv1.Synthesis{
						ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-slice",
				},
				Spec: apiv1.ResourceSliceSpec{
					Attempt: 3,
				},
			},
			expected: false,
		},
		{
			name: "another attempt started for the same synthesis",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Attempts: 5,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					Attempt: 3,
				},
			},
			expected: true,
		},
		{
			name: "slice is referenced by composition",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
				},
			},
			slice: &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					CompositionGeneration: 1,
				},
			},
			expected: false, // assumes synthesisReferencesSlice returns true
		},
		{
			name: "synthesis terminated and composition is deleted",
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
			slice:    &apiv1.ResourceSlice{},
			expected: true,
		},
		{
			name: "synthesis terminated and slice referenced",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized:    &metav1.Time{Time: time.Now()},
						ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
			},
			expected: false,
		},
		{
			name: "synthesis terminated, different composition generation, same synthesis",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: &metav1.Time{Time: time.Now()},
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					CompositionGeneration: 2,
				},
			},
			expected: false,
		},
		{
			name: "synthesis terminated, same composition generation, different synthesis",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized: &metav1.Time{Time: time.Now()},
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					SynthesisUUID: "foo",
				},
			},
			expected: false,
		},
		{
			name: "synthesis terminated, newer composition generation, different synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 3,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized:                   &metav1.Time{Time: time.Now()},
						ObservedCompositionGeneration: 2,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					SynthesisUUID:         "foo",
					CompositionGeneration: 1,
				},
			},
			expected: true,
		},
		{
			name: "synthesis in-progress, newer composition generation, different synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 3,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						ObservedCompositionGeneration: 2,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					SynthesisUUID:         "foo",
					CompositionGeneration: 1,
				},
			},
			expected: true,
		},
		{
			name: "synthesis in-progress, newer composition generation, same synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						ObservedCompositionGeneration: 1,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					CompositionGeneration: 1,
				},
			},
			expected: false,
		},
		{
			name: "synthesis terminated, newer composition and synthesis generation, different synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 2,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized:                   &metav1.Time{Time: time.Now()},
						ObservedCompositionGeneration: 2,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					SynthesisUUID:         "foo",
					CompositionGeneration: 1,
				},
			},
			expected: true,
		},
		{
			name: "synthesis terminated, older composition generation, different synthesis",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 1,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Synthesized:                   &metav1.Time{Time: time.Now()},
						ObservedCompositionGeneration: 1,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{Name: "test-slice"},
				Spec: apiv1.ResourceSliceSpec{
					SynthesisUUID:         "foo",
					CompositionGeneration: 2,
				},
			},
			expected: false,
		},
		{
			name: "composition is deleted but synthesis not terminated",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: nil,
				},
			},
			slice: &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					CompositionGeneration: 1,
				},
			},
			expected: false,
		},
		{
			name: "slice is outdated, not referenced, and comp.Generation is higher",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Generation:        5,
				},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Attempts: 3,
					},
				},
			},
			slice: &apiv1.ResourceSlice{
				Spec: apiv1.ResourceSliceSpec{
					Attempt:               1,
					CompositionGeneration: 2,
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldDeleteSlice(tt.comp, tt.slice)
			assert.Equal(t, tt.expected, result)
		})
	}
}
