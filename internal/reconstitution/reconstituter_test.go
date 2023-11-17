package reconstitution

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestReconstituterIntegration(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	client := mgr.GetClient()

	r, err := newReconstituter(mgr.Manager)
	require.NoError(t, err)
	queue := workqueue.New()
	r.AddQueue(queue)
	mgr.Start(t)

	// Create one composition that has one synthesis of a single resource
	comp := &apiv1.Composition{}
	comp.Name = "test-composition"
	comp.Namespace = "default"
	require.NoError(t, client.Create(ctx, comp))

	one := int64(1)
	comp.Status.CurrentState = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
		ResourceSliceCount:            &one,
	}
	require.NoError(t, client.Status().Update(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.Spec.CompositionGeneration = comp.Generation
	slice.Spec.Resources = []apiv1.Manifest{{
		Manifest: `{"kind":"baz","apiVersion":"any","metadata":{"name":"foo","namespace":"bar"}}`,
	}}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, client.Create(ctx, slice))

	// Prove the resource was cached
	ref := &ResourceRef{
		Composition: types.NamespacedName{
			Name:      comp.Name,
			Namespace: comp.Namespace,
		},
		Name:      "foo",
		Namespace: "bar",
		Kind:      "baz",
	}
	testutil.Eventually(t, func() bool {
		_, exists := r.Get(ctx, ref, comp.Generation)
		return exists
	})

	// Remove the composition and confirm cache is purged
	require.NoError(t, client.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		_, exists := r.Get(ctx, ref, comp.Generation)
		return !exists
	})

	// The queue should have been populated
	assert.Equal(t, 1, queue.Len())
}
