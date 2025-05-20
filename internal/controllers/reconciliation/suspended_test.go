package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestSuspendedReconciliation verifies that reconciliation is skipped when a composition is suspended
func TestSuspendedReconciliation(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"data": map[string]any{
					"foo": "bar",
				},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Create the composition
	_, comp := writeGenericComposition(t, upstream)

	// Wait for the composition to be reconciled
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})

	// Verify the resource was created
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("v1")
	obj.SetKind("ConfigMap")
	obj.SetName("test-obj")
	obj.SetNamespace("default")
	err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)
	require.Equal(t, "bar", obj.Object["data"].(map[string]any)["foo"])

	// Now suspend the composition
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Suspend = true
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Change the data in the ConfigMap
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		obj.Object["data"] = map[string]any{"foo": "changed"}
		return downstream.Update(ctx, obj)
	})
	require.NoError(t, err)

	// Force a reconciliation attempt
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "TEST", Value: "force-resynth"}}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for the synthesis to occur (but reconciliation should be skipped)
	time.Sleep(time.Millisecond * 500)

	// The changed value should still be there as reconciliation is suspended
	err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)
	require.Equal(t, "changed", obj.Object["data"].(map[string]any)["foo"])
	
	// Now unsuspend the composition
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Suspend = false
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// The resource should be reconciled back to the original state
	testutil.Eventually(t, func() bool {
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return err == nil && obj.Object["data"].(map[string]any)["foo"] == "bar"
	})
}

// TestSuspendedResourceDeletion verifies that resources are not deleted when a composition is suspended
func TestSuspendedResourceDeletion(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.GetClient()

	var synthesizerFunc func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error)
	synthesizerFunc = func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj-deletion",
					"namespace": "default",
				},
				"data": map[string]any{
					"foo": "bar",
				},
			},
		}}
		return output, nil
	}

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		return synthesizerFunc(ctx, s, input)
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Create the composition
	_, comp := writeGenericComposition(t, upstream)

	// Wait for the composition to be reconciled
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})

	// Verify the resource was created
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("v1")
	obj.SetKind("ConfigMap")
	obj.SetName("test-obj-deletion")
	obj.SetNamespace("default")
	err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)
	require.Equal(t, "bar", obj.Object["data"].(map[string]any)["foo"])

	// Now suspend the composition
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Suspend = true
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Change the synthesizer function to return no resources
	synthesizerFunc = func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{}
		return output, nil
	}

	// Force a resynth
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "TEST", Value: "force-resynthesis"}}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for the synthesis to occur
	time.Sleep(time.Millisecond * 500)

	// The resource should still exist because the composition is suspended
	err = downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
	require.NoError(t, err)

	// Now unsuspend the composition
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.Suspend = false
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// The resource should eventually be deleted now that the composition is unsuspended
	testutil.Eventually(t, func() bool {
		err := downstream.Get(ctx, client.ObjectKeyFromObject(obj), obj)
		return client.IgnoreNotFound(err) == nil && err != nil
	})
}