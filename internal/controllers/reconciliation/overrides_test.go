package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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

func TestOverrideObject(t *testing.T) {
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
							{"path": "self.data", "value": {"foo": "val-1", "bar": "val-2"}}
						]`,
					},
				},
				"data": map[string]any{"baz": "not the value"},
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
		return cm.Data["foo"] == "val-1" && cm.Data["bar"] == "val-2" && cm.Data["baz"] == ""
	})
}

func TestOverrideAndReplace(t *testing.T) {
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
						"eno.azure.io/replace": "true",
						"eno.azure.io/overrides": `[
							{"path": "self.data", "value": {"foo": "val-1", "bar": "val-2"}}
						]`,
					},
				},
				"data": map[string]any{"baz": "not the value"},
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
		return cm.Data["foo"] == "val-1" && cm.Data["bar"] == "val-2" && cm.Data["baz"] == ""
	})
}

func TestOverrideManagedFields(t *testing.T) {
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
						"eno.azure.io/reconcile-interval": "20ms",
						"eno.azure.io/overrides": `[
							{"path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno"}
						]`,
					},
				},
				"data": map[string]any{"foo": "bar", "another": "baz"},
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

	// Configmap is populated with the defaults
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "bar"
	})

	// Override the values with another field manager (test client)
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.Data["foo"] = "baz"
		return mgr.DownstreamClient.Update(ctx, cm)
	})

	time.Sleep(time.Millisecond * 40) // wait at least one reconile interval

	// The overridden value should not be overwritten by Eno
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "baz"
	})

	// Remove the field entirely
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.Data = nil
		return mgr.DownstreamClient.Update(ctx, cm)
	})

	// Prove the value is repopulated
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["foo"] == "bar"
	})
}

func TestOverrideHyphenatedFieldNames(t *testing.T) {
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
							{"path": "self.data[\"hyphen-key\"]", "value": "double-quote-value"},
							{"path": "self.data['single-hyphen-key']", "value": "single-quote-value"},
							{"path": "self.metadata.annotations[\"custom-annotation\"]", "value": "annotation-value"},
							{"path": "self.data[\"env-var\"]", "value": "conditional-value", "condition": "self.data[\"enable-flag\"] == \"true\""},
							{"path": "self.data[\"polling-var\"]", "value": "polling-applied", "condition": "self.data[\"polling-trigger\"] != null"}
						]`,
						"eno.azure.io/reconcile-interval": "10ms",
					},
				},
				"data": map[string]any{
					"hyphen-key":        "original-value",
					"single-hyphen-key": "original-single-value",
					"regular":           "unchanged",
					"env-var":           "original-env-value",
					"enable-flag":       "true", // Start with condition enabled for conditional test
					"polling-var":       "original-polling-value",
					// polling-trigger not set initially for polling test
				},
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

	// Test 1: Basic hyphenated field overrides with different quote styles
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["hyphen-key"] == "double-quote-value" &&
			cm.Data["single-hyphen-key"] == "single-quote-value" &&
			cm.Data["regular"] == "unchanged" &&
			cm.Annotations["custom-annotation"] == "annotation-value"
	})

	// Test 2: Conditional overrides with hyphenated paths and conditions
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["env-var"] == "conditional-value" // condition should be true initially
	})

	// Test 3: Polling behavior - verify initial state (condition false)
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["polling-var"] == "original-polling-value" // no polling-trigger yet
	})

	// Enable the polling condition by setting the trigger
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.Data["polling-trigger"] = "present"
		return mgr.DownstreamClient.Update(ctx, cm)
	})
	require.NoError(t, err)

	// Verify the polling override is eventually applied
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["polling-var"] == "polling-applied"
	})
}

func TestOverrideContainerResources(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	requireSSA(t, mgr)
	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]any{
						"eno.azure.io/reconcile-interval": "10ms",
						"eno.azure.io/overrides": `[
						    { "path": "self.spec.template.spec.containers[name='foo'].resources.limits.cpu", "value": null, "condition": "has(self.spec.template.spec.containers[0].resources.limits.cpu) && !pathManagedByEno" },
						    { "path": "self.spec.template.spec.containers[name='foo'].resources.requests.cpu", "value": null, "condition": "has(self.spec.template.spec.containers[0].resources.requests.cpu) && !pathManagedByEno" }
						]`,
					},
				},
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]any{
							"foo": "bar",
						},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]any{
								"foo": "bar",
							},
						},
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name":  "foo",
									"image": "bar",
									"resources": map[string]any{
										"requests": map[string]any{
											"cpu": "5m",
										},
										"limits": map[string]any{
											"cpu": "10m",
										},
									},
								},
							},
						},
					},
				},
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

	// The initial value should be populated
	deploy := &appsv1.Deployment{}
	deploy.Name = "test-obj"
	deploy.Namespace = "default"
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(deploy), deploy)
		return deploy.Spec.Template.Spec.Containers[0].Resources.Limits["cpu"].Equal(resource.MustParse("10m"))
	})

	// Simulate another client updating the resources value
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(deploy), deploy)
		if err != nil {
			return err
		}
		deploy.Spec.Template.Spec.Containers[0].Resources.Limits["cpu"] = resource.MustParse("20m")
		return mgr.DownstreamClient.Update(ctx, deploy)
	})
	require.NoError(t, err)

	// Wait for sync to prove the field wasn't stomped on
	time.Sleep(time.Millisecond * 100)
	mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(deploy), deploy)
	assert.True(t, deploy.Spec.Template.Spec.Containers[0].Resources.Limits["cpu"].Equal(resource.MustParse("20m")))
	assert.True(t, deploy.Spec.Template.Spec.Containers[0].Resources.Requests["cpu"].Equal(resource.MustParse("5m")))
}

func TestOverrideReplaceAnnotation(t *testing.T) {
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
						"eno.azure.io/overrides": `[{"path": "self.metadata.annotations['eno.azure.io/replace']", "value": "true", "condition": "self.metadata.annotations['should-replace'].startsWith('tru')"}]`,
						"unrelated":              "annotation", // just to avoid a nil map
					},
				},
				"data": map[string]any{"eno-field": "eno-value"},
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

	// Another client adds a field and signals replacement
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.Annotations["should-replace"] = "true"
		cm.Data["other-client-field"] = "other-value"
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

	// The resource should have been replaced
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		_, ok := cm.Data["other-client-field"]
		return !ok
	})

	// Add a field again - this time without replacement
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.Data["other-client-field"] = "other-value"
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

	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm))
	assert.Equal(t, "other-value", cm.Data["other-client-field"])
}

func TestOverrideDeletionModeAnnotation_RemoveFromSynth(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		if s.Spec.Image == "empty" {
			return output, nil
		}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]any{
						"eno.azure.io/overrides": `[{"path": "self.metadata.annotations['eno.azure.io/disable-reconciliation']", "value": "true", "condition": "string(composition.metadata.annotations['orphan-resources']).split(',').filter(val, val == 'this-resource').size() > 0"}]`,
					},
				},
				"data": map[string]any{"eno-field": "eno-value"},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)
	synth, comp := writeGenericComposition(t, upstream)

	// Wait for initial reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Confirm that the resource was created
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm))

	// Set the annotation on the composition to signal that resource should be orphaned
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Annotations = map[string]string{"orphan-resources": "some-other-resource,this-resource"}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Make the synth stop returning the resource and prove that is is not deleted by Eno
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(synth), synth)
		synth.Spec.Image = "empty"
		return upstream.Update(ctx, synth)
	})
	require.NoError(t, err)
	testutil.Eventually(t, func() bool {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		syn := comp.Status.CurrentSynthesis
		return syn != nil && syn.ObservedSynthesizerGeneration == synth.Generation
	})
	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm))
}

func TestOverrideDeletionModeAnnotation_DeleteComposition(t *testing.T) {
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
						"eno.azure.io/overrides": `[{"path": "self.metadata.annotations['eno.azure.io/disable-reconciliation']", "value": "true", "condition": "string(composition.metadata.annotations['orphan-resources']).split(',').filter(val, val == 'this-resource').size() > 0"}]`,
					},
				},
				"data": map[string]any{"eno-field": "eno-value"},
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

	// Confirm that the resource was created
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm))

	// Set the annotation on the composition to signal that resource should be orphaned
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Annotations = map[string]string{"orphan-resources": "some-other-resource,this-resource"}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Delete the composition and prove that the resource is not deleted by Eno
	require.NoError(t, upstream.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	})
	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm))
}

// TestOverrideTransferResource covers the basics of transferring a resource from one synth to another.
// It doesn't attempt to verify that conflicting writes are not made by Eno - only that things eventually converge on the right state.
// The temporal logic is verified through other tests.
func TestOverrideTransferResource(t *testing.T) {
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
						"eno.azure.io/reconcile-interval":     "2ms",
						"eno.azure.io/disable-reconciliation": "true",
						"eno.azure.io/overrides":              `[{"path": "self.metadata.annotations['eno.azure.io/disable-reconciliation']", "value": "false", "condition": "composition.metadata.annotations['current-winner'] == 'yes'"}]`,
						"synthName":                           s.Name,
					},
				},
			},
		}}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)

	syn1 := &apiv1.Synthesizer{}
	syn1.Name = "test-synth-1"
	syn1.Spec.Image = "test-image"
	require.NoError(t, upstream.Create(ctx, syn1))

	syn2 := &apiv1.Synthesizer{}
	syn2.Name = "test-synth-2"
	syn2.Spec.Image = "test-image"
	require.NoError(t, upstream.Create(ctx, syn2))

	symph := &apiv1.Symphony{}
	symph.Name = "test-symphony"
	symph.Namespace = "default"
	symph.Spec.Variations = []apiv1.Variation{
		{
			Annotations: map[string]string{"current-winner": ""},
			Synthesizer: apiv1.SynthesizerRef{Name: syn1.Name},
		},
		{
			Annotations: map[string]string{"current-winner": "yes"},
			Synthesizer: apiv1.SynthesizerRef{Name: syn2.Name},
		},
	}
	require.NoError(t, upstream.Create(ctx, symph))

	// Wait for initial reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph) == nil && symph.Status.ObservedGeneration == symph.Generation && symph.Status.Ready != nil
	})

	// Confirm that the resource is managed by the expected synthesizer
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return syn2.Name == cm.Annotations["synthName"]
	})

	// Update the symphony to flip ownership over to the other synth
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph)
		symph.Spec.Variations[0].Annotations = map[string]string{"current-winner": "yes"}
		symph.Spec.Variations[1].Annotations = map[string]string{"current-winner": ""}
		return upstream.Update(ctx, symph)
	})
	require.NoError(t, err)
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(symph), symph) == nil && symph.Status.ObservedGeneration == symph.Generation && symph.Status.Ready != nil
	})

	// Prove that the resource converges on the expected value
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return syn1.Name == cm.Annotations["synthName"]
	})
}

func TestMigratingFieldManagers(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	requireSSA(t, mgr)
	registerControllers(t, mgr)

	// Use a variable to change Eno's desired state during resynthesis
	enoValue := "eno-value"
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
				"data": map[string]any{"foo": enoValue},
			},
		}}
		return output, nil
	})

	// Setup with migrating field managers
	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                mgr.Manager,
		Timeout:                time.Minute,
		ReadinessPollInterval:  time.Hour,
		DisableServerSideApply: mgr.NoSsaSupport,
		MigratingFieldManagers: []string{"legacy-tool"},
	})
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// Wait for initial reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Resource should be created with Eno as the field manager
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return err == nil && cm.Data["foo"] == "eno-value"
	})

	// Simulate a legacy tool taking ownership of a field by updating managed fields
	// This simulates the scenario where a field was previously managed by another tool
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.ManagedFields = nil
		cm.Data["bar"] = "legacy-value"
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap"
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("legacy-tool"))
	})
	require.NoError(t, err)

	// Verify the field is owned by legacy-tool
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		for _, entry := range cm.GetManagedFields() {
			if entry.Manager == "legacy-tool" {
				return true
			}
		}
		return false
	})

	// Change Eno's desired state and force a resynthesis to trigger field manager migration
	enoValue = "eno-value-updated"
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "TRIGGER", Value: "resynthesis"}}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// Verify that Eno has taken ownership from legacy-tool
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		hasEno := false
		hasLegacy := false
		for _, entry := range cm.GetManagedFields() {
			if entry.Manager == "eno" {
				hasEno = true
			}
			if entry.Manager == "legacy-tool" {
				hasLegacy = true
			}
		}
		// Eno should have taken ownership, and legacy-tool should no longer own fields
		// (or should own an empty set of fields)
		// Also verify the value was updated
		return hasEno && !hasLegacy && cm.Data["foo"] == "eno-value-updated"
	})
}

func TestMigratingFieldManagersFieldRemoval(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	requireSSA(t, mgr)
	registerControllers(t, mgr)

	// Start with synthesizer that includes a field
	includeField := true
	fooValue := "eno-value"
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		data := map[string]any{"foo": fooValue}
		if includeField {
			data["bar"] = "eno-bar-value"
		}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"data": data,
			},
		}}
		return output, nil
	})

	// Setup with migrating field managers
	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                mgr.Manager,
		Timeout:                time.Minute,
		ReadinessPollInterval:  time.Hour,
		DisableServerSideApply: mgr.NoSsaSupport,
		MigratingFieldManagers: []string{"legacy-tool"},
	})
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
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return err == nil && cm.Data["bar"] == "eno-bar-value"
	})

	// Simulate legacy tool taking ownership of the "bar" field
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		if err != nil {
			return err
		}
		cm.ManagedFields = nil
		cm.Data["bar"] = "legacy-bar-value"
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap"
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("legacy-tool"))
	})
	require.NoError(t, err)

	// Verify legacy-tool owns the field
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return cm.Data["bar"] == "legacy-bar-value"
	})

	// Now remove the field from Eno's desired state and change foo to trigger reconciliation
	includeField = false
	fooValue = "eno-value-updated"

	// Force resynthesis
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "REMOVE_FIELD", Value: "true"}}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)

	// Wait for reconciliation
	testutil.Eventually(t, func() bool {
		return upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})

	// The critical test: since Eno took ownership from legacy-tool,
	// it should be able to remove the field even though it was originally owned by legacy-tool.
	// This is the whole point of the migration feature - to allow safe field removal during migrations.
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		_, exists := cm.Data["bar"]
		return !exists && cm.Data["foo"] == "eno-value-updated" // Field should be removed and foo should be updated
	})

	// Verify foo is still present with the updated value
	require.NoError(t, mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm))
	assert.Equal(t, "eno-value-updated", cm.Data["foo"])
}
