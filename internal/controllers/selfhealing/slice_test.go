package selfhealing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/flowcontrol"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

var testSynthesisConfig = &synthesis.Config{
	SliceCreationQPS: 15,
	PodNamespace:     "default",
	ExecutorImage:    "test-image",
}

func TestSliceRecreation(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewSliceController(mgr.Manager))
	require.NoError(t, rollout.NewController(mgr.Manager, time.Microsecond*10))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 1, time.Microsecond*10))

	testNS := "default"
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		cm := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-cm",
					"namespace": testNS,
				},
			},
		}
		output := &krmv1.ResourceList{Items: []*unstructured.Unstructured{cm}}
		return output, nil
	})
	mgr.Start(t)

	// Create synthesizer
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-image"
	require.NoError(t, mgr.GetClient().Create(ctx, syn))

	// Create composition
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = testNS
	comp.Spec.Synthesizer = apiv1.SynthesizerRef{Name: syn.Name}
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	// Get the composition for resource slice owner ref
	testutil.Eventually(t, func() bool {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil
	})

	// Create resource slice
	readyTime := metav1.Now()
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = testNS
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: &readyTime, Reconciled: true}}
	ownerRef := metav1.OwnerReference{
		APIVersion: comp.GetObjectKind().GroupVersionKind().Version,
		Kind:       comp.GetObjectKind().GroupVersionKind().Kind,
		Name:       comp.GetName(),
		UID:        comp.GetUID(),
		Controller: ptr.To(true),
	}
	slice.SetOwnerReferences(append(slice.GetOwnerReferences(), ownerRef))
	require.NoError(t, mgr.GetClient().Create(ctx, slice))

	// Synthesis has completed with resource slice ref
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			Synthesized:                   ptr.To(metav1.Now()),
			ObservedCompositionGeneration: comp.Generation,
			ResourceSlices:                []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
			UUID:                          "test-uuid",
		}
		return mgr.GetClient().Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Check resource slice is existed before deletion
	testutil.Eventually(t, func() bool {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice)
		return err == nil
	})
	// Remove the finalizer and delete the resource slice
	slice.SetAnnotations(map[string]string{})
	require.NoError(t, mgr.GetClient().Update(ctx, slice))
	require.NoError(t, mgr.GetClient().Delete(ctx, slice))

	// Check the the resource slice referenced by composition is missing
	testutil.Eventually(t, func() bool {
		for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
			slice := &apiv1.ResourceSlice{}
			slice.Name = ref.Name
			slice.Namespace = comp.Namespace
			err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice)
			if errors.IsNotFound(err) {
				return true
			}
		}
		return false
	})

	// Wait for the composition is re-synthesized
	testutil.Eventually(t, func() bool {
		require.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp))
		return comp.Status.PendingResynthesis == nil &&
			comp.Status.CurrentSynthesis != nil &&
			comp.Status.CurrentSynthesis.Synthesized != nil &&
			comp.Status.CurrentSynthesis.ResourceSlices != nil
	})

	// Check there is no resource slice referenced by composition missing
	testutil.Eventually(t, func() bool {
		require.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp))
		// Must have at least one resource slice ref
		if len(comp.Status.CurrentSynthesis.ResourceSlices) == 0 {
			return false
		}
		for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
			rs := &apiv1.ResourceSlice{}
			rs.Name = ref.Name
			rs.Namespace = comp.Namespace
			err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(rs), rs)
			if err != nil {
				return false
			}
			// The re-creation resource slice's name prefix is composition name by design.
			if !strings.HasPrefix(rs.Name, comp.Name) {
				return false
			}
		}
		return true
	})
}

func TestRequeueForPodTimeout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	mgr.Start(t)

	testNS := "default"
	// Create synthesizer
	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-image"
	syn.Spec.PodTimeout = &metav1.Duration{Duration: PodTimeout}
	require.NoError(t, mgr.GetClient().Create(ctx, syn))

	// Create composition
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = testNS
	comp.Spec.Synthesizer = apiv1.SynthesizerRef{Name: syn.Name}
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	// Create resource slice
	readyTime := metav1.Now()
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = testNS
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: &readyTime, Reconciled: true}}
	require.NoError(t, mgr.GetClient().Create(ctx, slice))

	// Check the both composition and synthesizer are existed before reconciliation
	testutil.Eventually(t, func() bool {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if err != nil {
			return false
		}
		err = mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(syn), syn)
		if err != nil {
			return false
		}
		return true
	})

	// Reconcile the resource slice controller to re-create the missing resource slice
	s := &sliceController{client: mgr.GetClient()}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	res, err := s.Reconcile(ctx, req)
	require.NoError(t, err)

	// Request should be requeue due to composition CurrentSynthesis is emtpy and not eligible for resynthesis
	assert.True(t, res.Requeue)
	assert.Equal(t, syn.Spec.PodTimeout.Duration, res.RequeueAfter)
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
