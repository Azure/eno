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

	// It should be able to become ready
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation
	})

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

	latestGen := comp.Generation
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration >= latestGen
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Prove the resource was reconciled correctly
	err = mgr.DownstreamClient.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Labels)
	assert.Equal(t, map[string]string{"foo": "bar", "baz": "qux"}, cm.Data)
}
