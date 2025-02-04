package resourceslice

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cache := resource.NewCache(nil)
	require.NoError(t, NewController(mgr.Manager, cache))
	mgr.Start(t)
	cli := mgr.GetClient()

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = comp.Namespace
	slice.Spec.Resources = []apiv1.Manifest{
		{Manifest: "resource-1"},
		{Manifest: "resource-2", Deleted: true},
	}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, cli.Create(ctx, slice))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID:           "test-synthesis",
		Initialized:    ptr.To(metav1.Now()),
		Synthesized:    ptr.To(metav1.Now()),
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// TODO: Reconcile resources, update the slice status with the result
}
