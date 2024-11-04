package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestPatchCreation proves that a patch resource will not be created if it doesn't exist.
func TestPatchCreation(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"ops": []map[string]any{
						{"op": "add", "path": "/data", "value": "foo"},
					},
				},
			},
		}
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{obj}}, nil
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

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.True(t, errors.IsNotFound(err))
}

// TestPatchDeletion proves that a patch resource can delete the resource it references.
func TestPatchDeletion(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"ops": []map[string]any{
						{"op": "add", "path": "/metadata/deletionTimestamp", "value": "2024-01-22T19:13:15Z"},
					},
				},
			},
		}
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{obj}}, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	require.NoError(t, downstream.Create(ctx, cm))

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.True(t, errors.IsNotFound(err))
}

// TestPatchDeletionBeforeCreation proves that a patch resource can delete the resource it references before the resource is created.
// Basically, this is the same behavior as Helm hook event with delete policy "before-hook-creation".
func TestPatchDeletionBeforeCreation(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Test configmap setup
	cmName := "test-obj"
	cmNamespace := "default"
	key := "foo"
	val := "bar"
	cm := &corev1.ConfigMap{}
	cm.Name = cmName
	cm.Namespace = cmNamespace

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		cm := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      cmName,
					"namespace": cmNamespace,
					"annotations": map[string]string{
						"eno.azure.io/readiness-group": "2",
					},
				},
				"data": map[string]any{
					key: val,
				},
			},
		}
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
					"annotations": map[string]string{
						// This patch should be applied before the configmap is created.
						"eno.azure.io/readiness-group": "1",
					},
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"ops": []map[string]any{
						{"op": "add", "path": "/metadata/deletionTimestamp", "value": "2024-01-22T19:13:15Z"},
					},
				},
			},
		}
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{obj, cm}}, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	// Verify the configmap should be created after the deletion patch is applied.
	err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)
	require.NotNil(t, cm)
	require.Equal(t, val, cm.Data[key])
}

// TestPatchDeletionBeforeUpgrade proves that a patch resource can delete the resource it references before the resource is upgraded.
// Basically, this is the same behavior as Helm hook event with delete policy "before-hook-creation".
func TestPatchDeletionBeforeUpgrade(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Test configmap setup
	cmName := "test-obj"
	cmNamespace := "default"
	key := "foo"
	val := "bar"
	cm := &corev1.ConfigMap{}
	cm.Name = cmName
	cm.Namespace = cmNamespace
	cm.Annotations = map[string]string{
		"eno.azure.io/readiness-group": "2",
	}

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":      cmName,
					"namespace": cmNamespace,
					"annotations": map[string]string{
						// This patch should be applied before the configmap is re-created.
						"eno.azure.io/readiness-group": "1",
					},
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"ops": []map[string]any{
						{"op": "add", "path": "/metadata/deletionTimestamp", "value": "2024-01-22T19:13:15Z"},
					},
				},
			},
		}

		// Update the configmap by adding new annotation
		cm := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      cmName,
					"namespace": cmNamespace,
					"annotations": map[string]string{
						"eno.azure.io/readiness-group": "2",
					},
				},
				"data": map[string]any{
					key: val,
				},
			},
		}
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{obj, cm}}, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Create configmap first before the patch deletion is applied.
	require.NoError(t, downstream.Create(ctx, cm))
	creationUID := cm.GetUID()

	// Create deletion patch and configmap with new change.
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)

	recreationUID := cm.GetUID()
	// Verify the configmap is re-created with new uid and data.
	require.NotEqual(t, creationUID, recreationUID)
	require.Equal(t, val, cm.Data[key])
}

// TestPatchDeletionForResourceWithReconciliationFromInput proves that a patch resource won't be triggered to
// delete the resource with reconcile-interval it references if the patch with lower readiness group
func TestPatchDeletionForResourceWithReconciliationFromInput(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	// Test configmap setup
	cmName := "test-obj"
	cmNamespace := "default"

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":      cmName,
					"namespace": cmNamespace,
					"annotations": map[string]string{
						// This patch should be applied before the configmap is re-created.
						"eno.azure.io/readiness-group": "1",
					},
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"ops": []map[string]any{
						{"op": "add", "path": "/metadata/deletionTimestamp", "value": "2024-01-22T19:13:15Z"},
					},
				},
			},
		}

		cm := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      cmName,
					"namespace": cmNamespace,
					"annotations": map[string]string{
						"eno.azure.io/readiness-group":    "2",
						"eno.azure.io/reconcile-interval": "1ms",
					},
				},
			},
		}

		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{obj, cm}}, nil
	})

	// Test subject
	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Create deletion patch and configmap.
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	cm := &corev1.ConfigMap{}
	cm.Name = cmName
	cm.Namespace = cmNamespace
	err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)

	uid := cm.GetUID()
	// Ensure the configmap is reconciled at least once.
	time.Sleep(100 * time.Millisecond)
	err = downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)

	newUID := cm.GetUID()
	// Verify the configmap is not re-created.
	require.Equal(t, uid, newUID)
}

// TestCleanupPatch proves that deletion patches can be used to cleanup unmanaged resources after the composition is deleted.
func TestCleanupPatch(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "eno.azure.io/v1",
				"kind":       "Patch",
				"metadata": map[string]any{
					"name":        "test-obj",
					"namespace":   "default",
					"annotations": map[string]any{"eno.azure.io/only-during-deletion": "true"},
				},
				"patch": map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"ops": []map[string]any{
						{"op": "add", "path": "/metadata/deletionTimestamp", "value": "2024-01-22T19:13:15Z"},
					},
				},
			},
		}
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{obj}}, nil
	})

	cm := &corev1.ConfigMap{}
	cm.Name = "test-obj"
	cm.Namespace = "default"
	require.NoError(t, downstream.Create(ctx, cm))

	setupTestSubject(t, mgr)
	mgr.Start(t)
	_, comp := writeGenericComposition(t, upstream)

	// The cm should still exist after the composition becomes ready
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})
	require.NoError(t, downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm))

	// The cm should be deleted when the composition is deleted
	require.NoError(t, upstream.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm))
	})
}
