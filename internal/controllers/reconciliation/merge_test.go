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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestDisableSSA proves that basic forward reconciliation still works when server-side apply is disabled.
func TestDisableSSA(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

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
									"ports": []any{
										map[string]any{"containerPort": 8080},
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

	// Test subject
	setupTestSubjectForOptions(t, mgr, Options{
		Manager:                mgr.Manager,
		Timeout:                time.Minute,
		ReadinessPollInterval:  time.Hour,
		DisableServerSideApply: true,
	})
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)
	waitForReadiness(t, mgr, comp, nil, nil)

	// Add a field to the deployment
	dep := &appsv1.Deployment{}
	dep.Name = "test-obj"
	dep.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)
		dep.Spec.ProgressDeadlineSeconds = ptr.To(int32(10))
		return mgr.DownstreamClient.Update(ctx, dep)
	})
	require.NoError(t, err)

	// Resynthesize to guarantee that Eno has reconciled the resource after the mutation
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "FORCE_SYNTHESIS", Value: "true"}}
		return upstream.Update(ctx, comp)
	})
	require.NoError(t, err)
	waitForReadiness(t, mgr, comp, nil, nil)

	// Prove the field wasn't removed
	testutil.Eventually(t, func() bool {
		err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(dep), dep)
		return err == nil && dep.Spec.ProgressDeadlineSeconds != nil && *dep.Spec.ProgressDeadlineSeconds == 10
	})
}

// TestRemoveProperty proves that properties can be removed as part of the three-way merge.
func TestRemoveProperty(t *testing.T) {
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
					"labels":    map[string]string{"foo": "bar", "baz": "qux"},
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		if s.Spec.Image == "updated" {
			output.Items[0].SetLabels(map[string]string{"foo": "bar"})
			output.Items[0].Object["data"] = map[string]string{"baz": "qux"}
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Sanity check the current state of the CM
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Labels)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Data)

	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	// Update
	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "bar"}, cm.Labels)
	assert.Equal(t, map[string]string{"baz": "qux"}, cm.Data)
}

// TestRemovePropertyAndOwnership is identical to TestRemoveProperty, but also removes the field ownership metadata.
func TestRemovePropertyAndOwnership(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
					"labels":    map[string]string{"foo": "bar", "baz": "qux"},
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		if s.Spec.Image == "updated" {
			output.Items[0].SetLabels(map[string]string{"foo": "bar"})
			output.Items[0].Object["data"] = map[string]string{"baz": "qux"}
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Remove the field ownership metadata
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	require.NoError(t, mgr.DownstreamClient.Patch(ctx, cm, client.RawPatch(types.JSONPatchType, []byte(`[{"op": "replace", "path": "/metadata/managedFields", "value": [{}]}]`))))

	// Sanity check the current state of the CM
	err := mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Labels)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Data)
	assert.Nil(t, cm.ManagedFields)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return len(cm.Labels) == 1 && len(cm.Data) == 1
	})
}

// TestRemovePropertyAndPartialOwnership is similar to TestRemovePropertyAndOwnership but instead of removing all
// field manager metadata it only removes one property. This proves that the pruning behavior works correctly when
// a subset of fields have been mutated by other clients.
func TestRemovePropertyAndPartialOwnership(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
					"labels":    map[string]string{"foo": "bar", "baz": "qux"},
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		if s.Spec.Image == "updated" {
			output.Items[0].SetLabels(map[string]string{"foo": "bar"})
			output.Items[0].Object["data"] = map[string]string{"baz": "qux"}
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Mutate one of the fields using another field manager
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.Data["foo"] = "new-value"
		return mgr.DownstreamClient.Update(ctx, cm)
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return len(cm.Labels) == 1 && len(cm.Data) == 1
	})
}

// TestRemoveMissingProperty proves that Eno's field manager logic doesn't unnecessarily update the field manager
// when pruning fields that don't currently have a value.
func TestRemoveMissingProperty(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		if s.Spec.Image == "updated" {
			output.Items[0].Object["data"] = map[string]string{"baz": "qux"}
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Remove the data field from outside of Eno
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		delete(cm.Data, "foo")
		return mgr.DownstreamClient.Update(ctx, cm)
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	testutil.Eventually(t, func() bool {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return len(cm.Data) == 1
	})
}

// TestReplaceProperty proves that a property can be overridden by another field owner using SSA,
// and Eno will still eventually reconcile it into the expected state.
func TestReplaceProperty(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
					"labels":    map[string]string{"foo": "bar", "baz": "qux"},
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Force update some of the properties with another field manager
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.ManagedFields = nil
		cm.Labels = map[string]string{"foo": "wrong-value"}
		cm.Data = map[string]string{"baz": "wrong-value"}
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap" // the downstream client doesn't have a scheme
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("test"))
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "anything"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Labels)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Data)
}

// TestReplacePropertyAndRemoveOwnership is identical to TestReplaceProperty, but also removes the field ownership metadata.
func TestReplacePropertyAndRemoveOwnership(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
					"labels":    map[string]string{"foo": "bar", "baz": "qux"},
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Force update some of the properties with another field manager
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.ManagedFields = nil
		cm.Labels = map[string]string{"foo": "wrong-value"}
		cm.Data = map[string]string{"baz": "wrong-value"}
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap" // the downstream client doesn't have a scheme
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("test"))
	})
	require.NoError(t, err)

	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		for _, cur := range cm.ManagedFields {
			if cur.Manager == "eno" {
				cur.FieldsV1 = nil
				break
			}
		}
		return mgr.DownstreamClient.Update(ctx, cm)
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "anything"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Labels)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Data)
}

// TestExternallyManagedPropertyPreserved proves that a property not managed by Eno (due to an override)
// will not be stomped on when Eno modifies other properties in the same resource.
func TestExternallyManagedPropertyPreserved(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
						"eno.azure.io/overrides": `[{ "path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno" }]`,
					},
				},
				"data": map[string]any{"foo": "bar", "baz": "qux"},
			},
		}}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Hand off ownership of 'foo'
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.ManagedFields = nil
		cm.Data["foo"] = "another-value"
		cm.Data["baz"] = "updated-to-force-apply" // this is important - otherwise resources will match and skip reconciliation
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap" // the downstream client doesn't have a scheme
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("test"))
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "anything"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "another-value", "baz": "qux"}, cm.Data)
}

// TestExternallyManagedPropertyRemoved proves that removing a property not managed by Eno (due to an override)
// will not cause Eno to take back its ownership and cause the property to be pruned.
func TestExternallyManagedPropertyRemoved(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
						"eno.azure.io/overrides": `[{ "path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno" }]`,
					},
				},
				"data": map[string]any{"foo": "bar"},
			},
		}}
		if s.Spec.Image == "updated" {
			delete(output.Items[0].Object, "data")
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Hand off ownership of 'foo'
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.ManagedFields = nil
		cm.Data["foo"] = "another-value"
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap" // the downstream client doesn't have a scheme
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("test"))
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "another-value"}, cm.Data)
}

// TestExternallyManagedPropertyAndOverrideRemoved is the negative case of TestExternallyManagedPropertyRemoved.
// It proves that removing a property managed by another field manager AND removing the override that allowed it
// will cause the property to be pruned from the resource.
func TestExternallyManagedPropertyAndOverrideRemoved(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	requireSSA(t, mgr)
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
						"eno.azure.io/overrides": `[{ "path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno" }]`,
					},
				},
				"data": map[string]any{"foo": "bar"},
			},
		}}
		if s.Spec.Image == "updated" {
			delete(output.Items[0].Object, "data")
			output.Items[0].SetAnnotations(map[string]string{
				// NOTE(jordan): The test will fail if we don't positively change a value.
				// This is because of the dryrun/current comparison optimization
				// i.e. because from Eno's perspective nothing changes between the current and next version.
				// From it's perspective the field being removed was already null due to the override.
				//
				// We may want to add special handling for this, but honestly I think it's better
				// that we just document it as a known shortcoming. Pruning a property not managed by
				// Eno without also modifying another property is... not going to happen every day.
				// And this fix to the comparison logic would be costly/complicated.
				"anything": "foo",
			})
		}
		return output, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// Creation
	waitForReadiness(t, mgr, comp, syn, nil)

	// Hand off ownership of 'foo'
	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		cm.ManagedFields = nil
		cm.Data["foo"] = "another-value"
		cm.APIVersion = "v1"
		cm.Kind = "ConfigMap" // the downstream client doesn't have a scheme
		return mgr.DownstreamClient.Patch(ctx, cm, client.Apply, client.ForceOwnership, client.FieldOwner("test"))
	})
	require.NoError(t, err)

	// Update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "updated"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	waitForReadiness(t, mgr, comp, syn, nil)

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Len(t, cm.Data, 0)
}
