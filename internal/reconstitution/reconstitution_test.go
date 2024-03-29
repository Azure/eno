package reconstitution

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestManagerBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	client := mgr.GetClient()

	cache := NewCache(client)
	tr := &testReconciler{cache: cache}
	err := New(mgr.Manager, cache, tr)
	require.NoError(t, err)

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
	tr.comp = NewCompositionRef(comp)

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{
		Manifest: `{"kind":"baz","apiVersion":"any","metadata":{"name":"foo","namespace":"bar"}}`,
	}}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, client.Create(ctx, slice))

	// The resource is eventually reconciled
	testutil.Eventually(t, func() bool {
		res := tr.lastResource.Load()
		return res != nil && res.Ref.Name == "foo"
	})
}

type testReconciler struct {
	cache        *Cache
	comp         *CompositionRef
	lastResource atomic.Pointer[Resource]
}

func (t *testReconciler) Reconcile(ctx context.Context, req *Request) (ctrl.Result, error) {
	resource, exists := t.cache.Get(ctx, t.comp, &req.Resource)
	if !exists {
		panic("resource should exist in cache")
	}
	t.lastResource.Store(resource)
	return ctrl.Result{}, nil
}
