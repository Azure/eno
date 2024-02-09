package synthesis

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestSliceCleanupControllerOrphanedSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewSliceCleanupController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, mgr.GetClient().Create(ctx, comp))

	// Synthesis has completed with no resulting resource slices
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		comp.Status.CurrentState = &apiv1.Synthesis{
			Synthesized: true,
		}
		return mgr.GetClient().Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// A random slice is created, but not part of the composition's synthesis
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = "default"
	slice.Finalizers = []string{"eno.azure.io/cleanup"}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	require.NoError(t, mgr.GetClient().Create(ctx, slice))

	// Slice should eventually be deleted
	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(mgr.GetClient().Get(ctx, client.ObjectKeyFromObject(slice), slice))
	})
}
