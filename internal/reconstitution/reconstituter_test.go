package reconstitution

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
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

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
		ResourceSlices:                []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
		Synthesized:                   ptr.To(metav1.Now()),
	}
	require.NoError(t, client.Status().Update(ctx, comp))
	compRef := NewCompositionRef(comp)

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{
		Manifest: `{"kind":"baz","apiVersion":"any","metadata":{"name":"foo","namespace":"bar"}}`,
	}}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, client.Create(ctx, slice))

	// Prove the resource was cached
	ref := &resource.Ref{
		Name:      "foo",
		Namespace: "bar",
		Kind:      "baz",
	}
	testutil.Eventually(t, func() bool {
		_, exists := r.Get(ctx, compRef, ref)
		return exists
	})

	// Remove the composition and confirm cache is purged
	require.NoError(t, client.Delete(ctx, comp))
	testutil.Eventually(t, func() bool {
		_, exists := r.Get(ctx, compRef, ref)
		return !exists
	})

	// The queue should have been populated
	assert.Equal(t, 1, queue.Len())
}
