package reconstitution

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestManagerBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	client := mgr.GetClient()

	rm, err := New(mgr.Manager, time.Millisecond)
	require.NoError(t, err)

	tr := &testReconciler{mgr: rm}
	rm.Add(tr)
	mgr.Start(t)

	// Create one composition that has one synthesis of a single resource
	comp := &apiv1.Composition{}
	comp.Name = "test-composition"
	comp.Namespace = "default"
	require.NoError(t, client.Create(ctx, comp))

	comp.Status.CurrentState = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
		ResourceSlices:                []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
		Synthesized:                   true,
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
	mgr          *Manager
	comp         *CompositionRef
	lastResource atomic.Pointer[Resource]
}

func (t *testReconciler) Name() string { return "testReconciler" }

func (t *testReconciler) Reconcile(ctx context.Context, req *Request) (ctrl.Result, error) {
	resource, exists := t.mgr.GetClient().Get(ctx, t.comp, &req.Resource)
	if !exists {
		panic("resource should exist in cache")
	}
	t.lastResource.Store(resource)
	return ctrl.Result{}, nil
}
