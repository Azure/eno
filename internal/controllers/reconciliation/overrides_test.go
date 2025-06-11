package reconciliation

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestOverrideBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

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
					"annotations": map[string]any{
						"eno.azure.io/overrides": `[{"path": "self.data.foo", "value": "eno-value", "condition": "!has(self.data.foo)"}]`,
					},
				},
				"data": map[string]any{"foo": "eno-value"},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// Wait for initial reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Simulate another client setting the field to a different value
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.Data["foo"] = "external-value"
		return mgr.DownstreamClient.Update(ctx, cm)
	})
	require.NoError(t, err)

	// Resynthesize to trigger reconciliation
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "FORCE_SYNTHESIS", Value: "true"}}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// The external value should still be present
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "external-value"
	})
}

func TestOverridePolling(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

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
					"annotations": map[string]any{
						"eno.azure.io/overrides":          `[{"path": "self.data.foo", "value": "eno-replaced-value", "condition": "self.data.bar == 'EnableOverride'"}]`,
						"eno.azure.io/reconcile-interval": "10ms",
					},
				},
				"data": map[string]any{"foo": "eno-value"},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// Wait for initial reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "eno-value"
	})

	// Enable the condition
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.Data["bar"] = "EnableOverride"
		return mgr.DownstreamClient.Update(ctx, cm)
	})
	require.NoError(t, err)

	// The override should eventually be applied
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "eno-replaced-value"
	})
}

func TestOverridePrecedence(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

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
					"annotations": map[string]any{
						"eno.azure.io/overrides": `[
							{"path": "self.data.foo", "value": "value-2"},
							{"path": "self.data.foo", "value": "value-3"}
						]`,
					},
				},
				"data": map[string]any{"foo": "value-1"},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "value-3"
	})
}
