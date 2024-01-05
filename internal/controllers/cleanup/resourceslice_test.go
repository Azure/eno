package cleanup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestSliceControllerHappyPath(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewResourceSliceController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	// Synthesis is in progress
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = "default"
	slice.Finalizers = []string{"eno.azure.io/cleanup"}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, mgr.GetClient().Create(ctx, slice))

	// Synthesis has completed
	comp.Status.CurrentState = &apiv1.Synthesis{
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
		Synthesized:    true,
	}
	require.NoError(t, mgr.GetClient().Status().Update(ctx, comp))

	// Slice should not be deleted
	time.Sleep(time.Millisecond * 50)
	testutil.Eventually(t, func() bool {
		return mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice) == nil && slice.DeletionTimestamp == nil
	})

	// Remove reference
	require.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp))
	comp.Status.CurrentState = &apiv1.Synthesis{Synthesized: true}
	comp.Status.PreviousState = &apiv1.Synthesis{Synthesized: true}
	require.NoError(t, mgr.GetClient().Status().Update(ctx, comp))

	// Slice should eventually be deleted
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice))
	})
}

func TestSliceControllerCompositionCleanup(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewResourceSliceController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = "default"
	slice.Finalizers = []string{"eno.azure.io/cleanup"}
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "test-resource"}}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, mgr.GetClient().Create(ctx, slice))

	comp.Status.CurrentState = &apiv1.Synthesis{
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
		Synthesized:    true,
	}
	require.NoError(t, mgr.GetClient().Status().Update(ctx, comp))
	require.NoError(t, mgr.GetClient().Delete(ctx, slice))

	// Slice should not be released
	time.Sleep(time.Millisecond * 50)
	assert.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Remove the resource
	slice.Status.Resources = []apiv1.ResourceState{{Deleted: true}}
	require.NoError(t, mgr.GetClient().Status().Update(ctx, slice))

	// Slice should eventually be released
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice))
	})
}
