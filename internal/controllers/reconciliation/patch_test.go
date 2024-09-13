package reconciliation

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	testv1 "github.com/Azure/eno/internal/controllers/reconciliation/fixtures/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestPatchCreation proves that a patch resource will not be created if it doesn't exist.
func TestPatchCreation(t *testing.T) {
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

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
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

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
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

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
	scheme := runtime.NewScheme()
	corev1.SchemeBuilder.AddToScheme(scheme)
	testv1.SchemeBuilder.AddToScheme(scheme)

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
	creationTime := cm.GetCreationTimestamp()
	creationResourceVersion := cm.GetResourceVersion()
	// Wait for more than one second to ensure the createTimestamp is changed when recreating configmap,
	// or it might have same createTimestamp and fail to pass test.
	time.Sleep(1500 * time.Millisecond)

	// Create deletion patch and configmap with new change.
	_, comp := writeGenericComposition(t, upstream)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
	require.NoError(t, err)

	recreationTime := cm.GetCreationTimestamp()
	recreationResourceVersion := cm.GetResourceVersion()
	// Verify the configmap is re-created with new creationTime, resourceVersion and data.
	require.True(t, creationTime.Before(&recreationTime))
	require.NotEqual(t, creationResourceVersion, recreationResourceVersion)
	require.Equal(t, val, cm.Data[key])
}
