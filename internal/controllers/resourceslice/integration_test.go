package resourceslice

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestResourceSliceLifecycle(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewCleanupController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		return cli.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil // wait for the informer
	})

	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = &apiv1.Synthesis{}
		return cli.Status().Update(ctx, comp)
	})

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: `{}`}}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = nil
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
		}
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		return cli.Get(ctx, client.ObjectKeyFromObject(slice), slice) == nil // wait for the informer
	})

	// The status of the resources in the slice should be aggregated into the composition
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		slice.Status.Resources = []apiv1.ResourceState{
			{Reconciled: true, Ready: ptr.To(metav1.Now())},
		}
		return cli.Status().Update(ctx, slice)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return comp.Status.CurrentSynthesis != nil &&
			comp.Status.CurrentSynthesis.Reconciled != nil &&
			comp.Status.CurrentSynthesis.Ready != nil
	})

	// Unused slices are deleted and missing slices cause resynthesis
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "another-slice"}},
		}
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		return errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	})
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return comp.ShouldForceResynthesis()
	})
}
