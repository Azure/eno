package reconciliation

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestPatchDeleteWhenCompositionIsDeleted proves that a patch resource can delete the resource it references
// when the composition is deleted with orphaning enabled.
func TestPatchDeleteWhenCompositionIsDeleted(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	downstream := mgr.DownstreamClient

	cmName := "test-cm"
	cmNamespace := "default"

	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{
			{
				Object: map[string]any{
					"apiVersion": "eno.azure.io/v1",
					"kind":       "Patch",
					"metadata": map[string]any{
						"name":      cmName,
						"namespace": cmNamespace,
					},
					"patch": map[string]any{
						"apiVersion": "v1",
						"kind":       "ConfigMap",
						"ops": []map[string]any{
							{"op": "add", "path": "/metadata/deletionTimestamp", "value": "deleted"},
						},
					},
				},
			},
		}
		return output, nil
	})

	setupTestSubject(t, mgr)
	mgr.Start(t)

	// Create a composition with orphan deletion strategy
	comp := &apiv1.Composition{}
	comp.Name = "test-comp-deletion"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "test-syn"
	comp.Annotations = map[string]string{
		"eno.azure.io/deletion-strategy": "orphan",
	}
	require.NoError(t, upstream.Create(ctx, comp))

	// Create the configmap that will be deleted by the patch
	cm := &corev1.ConfigMap{}
	cm.Name = cmName
	cm.Namespace = cmNamespace
	require.NoError(t, downstream.Create(ctx, cm))

	// Ensure the composition is created and reconciled
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Reconciled != nil
	})

	// Delete the composition
	require.NoError(t, upstream.Delete(ctx, comp))

	// Verify the composition is marked for deletion
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.DeletionTimestamp != nil
	})

	// The patch should still delete the configmap even though the composition is deleted with orphan strategy
	testutil.Eventually(t, func() bool {
		err := downstream.Get(ctx, client.ObjectKeyFromObject(cm), cm)
		return errors.IsNotFound(err)
	})
}