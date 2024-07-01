package reconciliation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/aggregation"
	"github.com/Azure/eno/internal/controllers/flowcontrol"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/controllers/rollout"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

// TestTerminalError proves that returning an error result from a synthesizer's KRM function will:
// - Not result in resource deletion (assuming no resources are returned)
// - Not cause any updates to resources that _are_ returned
// - Not prevent current resources from being deleted if removed during the next synthesis
func TestTerminalError(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	// Register supporting controllers
	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10))
	require.NoError(t, rollout.NewSynthesizerController(mgr.Manager))
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		if s.Spec.Image == "empty" {
			return output, nil
		}

		if s.Spec.Image == "create" {
			output.Items = []*unstructured.Unstructured{
				{
					Object: map[string]any{
						"apiVersion": "v1",
						"kind":       "ConfigMap",
						"metadata": map[string]any{
							"name":      "initial-obj-1",
							"namespace": "default",
						},
					},
				},
				{
					Object: map[string]any{
						"apiVersion": "v1",
						"kind":       "ConfigMap",
						"metadata": map[string]any{
							"name":      "initial-obj-2",
							"namespace": "default",
						},
					},
				},
			}
			return output, nil
		}

		output.Results = []*krmv1.Result{{
			Message:  "test error",
			Severity: "error",
		}}
		output.Items = []*unstructured.Unstructured{
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "updated-obj",
						"namespace": "default",
					},
				},
			},
			{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "initial-obj-2",
						"namespace": "default",
					},
					"data": map[string]string{"foo": "bar"},
				},
			},
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Wait for composition to become ready
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})

	// Update the synthesizer (this version will error out)
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := upstream.Get(context.Background(), client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated"
		return upstream.Update(context.Background(), syn)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation && comp.Status.CurrentSynthesis.Synthesized != nil
	})

	// Wait a bit in case the reconciliation controller does anything out of pocket
	time.Sleep(time.Millisecond * 500)

	// The object that didn't already exist wasn't created
	assert.True(t, errors.IsNotFound(mgr.DownstreamClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "updated-obj"}, &corev1.ConfigMap{})))

	// The object that wasn't returned wasn't deleted
	assert.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "initial-obj-1"}, &corev1.ConfigMap{}))

	// The object that existed wasn't updated
	cm := &corev1.ConfigMap{}
	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "initial-obj-1"}, cm))
	assert.Empty(t, cm.Data["foo"])

	// Run another synthesis - this time returning no resources
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := upstream.Get(context.Background(), client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "empty"
		return upstream.Update(context.Background(), syn)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Prove all resources were deleted - no state was lost due to the failed synthesis
	assert.True(t, errors.IsNotFound(mgr.DownstreamClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "updated-obj"}, &corev1.ConfigMap{})))
	assert.True(t, errors.IsNotFound(mgr.DownstreamClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "initial-obj-1"}, &corev1.ConfigMap{})))
	assert.True(t, errors.IsNotFound(mgr.DownstreamClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "initial-obj-2"}, &corev1.ConfigMap{})))

	// Prove that the failed synthesis isn't retained
	assert.Len(t, comp.Status.CurrentSynthesis.Results, 0)
	assert.Len(t, comp.Status.PreviousSynthesis.Results, 0)
}

// TestSliceCleanupOutdated proves there is an upper bound on how many resource slices
// can exist for a given composition even when it's stuck in a synthesis retry loop.
func TestSliceCleanupOutdated(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	// Register supporting controllers
	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10))
	require.NoError(t, rollout.NewSynthesizerController(mgr.Manager))
	require.NoError(t, rollout.NewController(mgr.Manager, time.Millisecond))
	require.NoError(t, synthesis.NewSliceCleanupController(mgr.Manager))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, defaultConf))
	require.NoError(t, aggregation.NewSliceController(mgr.Manager))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		return nil, fmt.Errorf("uh oh")
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "create"
	require.NoError(t, upstream.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, upstream.Create(ctx, comp))

	// Wait for a few attempts
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Attempts >= 3
	})

	// There should not be more than one set of resource slices
	testutil.Eventually(t, func() bool {
		sliceList := &apiv1.ResourceSliceList{}
		err := upstream.List(ctx, sliceList)
		return err == nil && len(sliceList.Items) < 2
	})
}
