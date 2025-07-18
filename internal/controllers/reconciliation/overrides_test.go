package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

func TestOverrideManagedFieldsDaemonset(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "apps/v1",
				"kind":       "DaemonSet",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"labels": map[string]any{
						"app": "test-app",
					},
					"annotations": map[string]any{
						"eno.azure.io/reconcile-interval": "20ms",
						"eno.azure.io/overrides": `[
							{"path": "self.spec.template.spec.containers[0].resources.requests.cpu", "value": null, "condition": "has(self.spec.template.spec.containers[0].resources.requests.cpu) && !pathManagedByEno"},
							{"path": "self.spec.template.spec.containers[0].resources.requests.memory", "value": null, "condition": "has(self.spec.template.spec.containers[0].resources.requests.memory) && !pathManagedByEno"},
							{"path": "self.spec.template.spec.containers[0].resources.limits.cpu", "value": null, "condition": "has(self.spec.template.spec.containers[0].resources.limits.cpu) && !pathManagedByEno"},
							{"path": "self.spec.template.spec.containers[0].resources.limits.memory", "value": null, "condition": "has(self.spec.template.spec.containers[0].resources.limits.memory) && !pathManagedByEno"}
						]`,
					},
				},
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]any{
							"app": "test-app",
						},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]any{
								"app": "test-app",
							},
						},
						"spec": map[string]any{
							"restartPolicy": "Always",
							"containers": []map[string]any{
								{
									"name":            "test-container",
									"image":           "nginx:latest",
									"imagePullPolicy": "IfNotPresent",
									"ports": []map[string]any{
										{
											"containerPort": 80,
											"protocol":      "TCP",
										},
									},
									"resources": map[string]any{
										"requests": map[string]any{
											"cpu":    "100m",
											"memory": "200Mi",
										},
										"limits": map[string]any{
											"cpu":    "500m",
											"memory": "700Mi",
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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if err != nil {
			t.Logf("Error getting composition: %v", err)
			return false
		}
		if comp.Status.CurrentSynthesis == nil {
			t.Logf("CurrentSynthesis is nil")
			return false
		}
		// For DaemonSet, we don't wait for Ready status since it requires nodes
		// Just wait for synthesis to be assigned and processing
		if comp.Status.CurrentSynthesis.UUID == "" {
			t.Logf("CurrentSynthesis.UUID is empty")
			return false
		}
		t.Logf("Composition synthesis is processing!")
		return true
	})

	// DaemonSet is populated with the defaults
	ds := &appsv1.DaemonSet{}
	ds.Name = "test-obj"
	ds.Namespace = "default"

	// First, just wait for the DaemonSet to be created
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		return err == nil
	})

	// Then check for the CPU value
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			t.Logf("Error getting DaemonSet: %v", err)
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 {
			t.Logf("No containers found in DaemonSet")
			return false
		}
		if ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			t.Logf("No resource requests found")
			return false
		}
		cpu, exists := ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
		if !exists {
			t.Logf("CPU resource not found")
			return false
		}
		t.Logf("Found CPU: %s", cpu.String())
		return cpu.String() == "100m"
	})

	// Override the CPU value with another field manager (test client)
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return err
		}
		if ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			ds.Spec.Template.Spec.Containers[0].Resources.Requests = make(corev1.ResourceList)
		}
		ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("205m")
		return mgr.DownstreamClient.Update(ctx, ds)
	})

	time.Sleep(time.Millisecond * 40) // wait at least one reconile interval

	// Override the memory value with another field manager (test client)
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return err
		}
		if ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			ds.Spec.Template.Spec.Containers[0].Resources.Requests = make(corev1.ResourceList)
		}
		ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("305Mi")
		return mgr.DownstreamClient.Update(ctx, ds)
	})

	time.Sleep(time.Millisecond * 40) // wait at least one reconile interval

	// Override the CPU limit with another field manager (test client)
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return err
		}
		if ds.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
			ds.Spec.Template.Spec.Containers[0].Resources.Limits = make(corev1.ResourceList)
		}
		ds.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse("605m")
		return mgr.DownstreamClient.Update(ctx, ds)
	})

	time.Sleep(time.Millisecond * 40) // wait at least one reconile interval

	// Override the memory limit with another field manager (test client)
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return err
		}
		if ds.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
			ds.Spec.Template.Spec.Containers[0].Resources.Limits = make(corev1.ResourceList)
		}
		ds.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = resource.MustParse("805Mi")
		return mgr.DownstreamClient.Update(ctx, ds)
	})

	time.Sleep(time.Millisecond * 40) // wait at least one reconile interval

	// The overridden CPU value should not be overwritten by Eno
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			return false
		}
		cpu := ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
		return cpu.String() == "205m"
	})

	// The overridden memory value should not be overwritten by Eno
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			return false
		}
		memory := ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
		return memory.String() == "305Mi"
	})

	// The overridden CPU limit should not be overwritten by Eno
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
			return false
		}
		cpu := ds.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
		return cpu.String() == "605m"
	})

	// The overridden memory limit should not be overwritten by Eno
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
			return false
		}
		memory := ds.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
		return memory.String() == "805Mi"
	})

	// Remove the resources entirely
	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return err
		}
		ds.Spec.Template.Spec.Containers[0].Resources.Requests = nil
		ds.Spec.Template.Spec.Containers[0].Resources.Limits = nil
		return mgr.DownstreamClient.Update(ctx, ds)
	})

	// Prove the CPU value is repopulated
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			return false
		}
		cpu := ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
		return cpu.String() == "100m"
	})

	// Prove the memory value is repopulated
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Requests == nil {
			return false
		}
		memory := ds.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
		return memory.String() == "200Mi"
	})

	// Prove the CPU limit is repopulated
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
			return false
		}
		cpu := ds.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
		return cpu.String() == "500m"
	})

	// Prove the memory limit is repopulated
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)
		if err != nil {
			return false
		}
		if len(ds.Spec.Template.Spec.Containers) == 0 || ds.Spec.Template.Spec.Containers[0].Resources.Limits == nil {
			return false
		}
		memory := ds.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory]
		return memory.String() == "700Mi"
	})
}
