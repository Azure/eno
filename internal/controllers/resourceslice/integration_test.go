package resourceslice

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	comp.Spec.Synthesizer = apiv1.SynthesizerRef{Name: "some-synthesizer"}
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		return cli.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil // wait for the informer
	})

	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
			UUID:           "test-uuid",
			Synthesized:    ptr.To(metav1.Now()),
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
			{Reconciled: true, Ready: ptr.To(metav1.NewTime(time.Now().Add(time.Minute)))},
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
			UUID:           "test-uuid",
			Synthesized:    ptr.To(metav1.Now()),
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

// I1: end-to-end propagation of per-resource blocker messages onto
// composition.currentSynthesis.conditions via the live controller.
func TestResourceSliceConditionPropagation(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewCleanupController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager))
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-cond-prop"
	comp.Namespace = "default"
	comp.Spec.Synthesizer = apiv1.SynthesizerRef{Name: "some-synthesizer"}
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		return cli.Get(ctx, client.ObjectKeyFromObject(comp), comp) == nil
	})

	retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "uuid-cond-prop"}
		return cli.Status().Update(ctx, comp)
	})

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-cond-prop"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "uuid-cond-prop"
	slice.Spec.Resources = []apiv1.Manifest{
		{Manifest: `{"kind":"Deployment","metadata":{"name":"foo"}}`},
	}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = nil
		comp.Status.CurrentSynthesis = &apiv1.Synthesis{
			UUID:           "uuid-cond-prop",
			Synthesized:    ptr.To(metav1.Now()),
			ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
		}
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		return cli.Get(ctx, client.ObjectKeyFromObject(slice), slice) == nil
	})

	// Mark the resource as not yet reconciled.
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		slice.Status.Resources = []apiv1.ResourceState{{Reconciled: false}}
		return cli.Status().Update(ctx, slice)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if comp.Status.CurrentSynthesis == nil {
			return false
		}
		c := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
		return c != nil && c.Status == metav1.ConditionFalse && c.Message == "Deployment/foo"
	})

	// Flip the resource to reconciled+ready, expect the condition to clear.
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		slice.Status.Resources = []apiv1.ResourceState{{Reconciled: true, Ready: ptr.To(metav1.Now())}}
		return cli.Status().Update(ctx, slice)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if comp.Status.CurrentSynthesis == nil {
			return false
		}
		c := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
		return c != nil && c.Status == metav1.ConditionTrue && c.Message == ""
	})
}
