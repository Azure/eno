package reconstitution

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
)

func TestManagerBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	client := mgr.GetClient()

	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[resource.Request]())
	cache := resource.NewCache(nil, queue)
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
		UUID:           uuid.NewString(),
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "test-slice"}},
		Synthesized:    ptr.To(metav1.Now()),
	}
	require.NoError(t, client.Status().Update(ctx, comp))
	tr.syn = NewSynthesisRef(comp)

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
	cache        *resource.Cache
	syn          *SynthesisRef
	lastResource atomic.Pointer[resource.Resource]
}

func (t *testReconciler) Reconcile(ctx context.Context, req *resource.Request) (ctrl.Result, error) {
	resource, exists := t.cache.Get(t.syn.UUID, &req.Resource)
	if !exists {
		panic("resource should exist in cache")
	}

	t.lastResource.Store(resource)
	return ctrl.Result{}, nil
}
