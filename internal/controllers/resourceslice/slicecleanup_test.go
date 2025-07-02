package resourceslice

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestSliceCleanupSliceReferences(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli, orphanCompSelector: labels.Nothing()}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	// Current synthesis references the slice, shouldn't be deleted
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Status().Update(ctx, comp))

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Synthesis no longer references the slice
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "different-slice"}}}
	require.NoError(t, cli.Status().Update(ctx, comp))

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))
}

func TestSliceCleanupOrphan(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli, orphanCompSelector: labels.Everything()}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	comp.Finalizers = []string{"anything.io/any-finalizer"}
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Finalizers = []string{"anything.io/any-finalizer"}
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Delete(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}}}
	require.NoError(t, cli.Status().Update(ctx, comp))

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)

	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))
}

func TestSliceCleanupInFlight(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	// Composition has an in-flight synthesis matching the resource slice - it shouldn't be deleted
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: slice.Spec.SynthesisUUID}
	require.NoError(t, cli.Status().Update(ctx, comp))

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Wrong UUID
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "wrong-uuid"}
	require.NoError(t, cli.Status().Update(ctx, comp))

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))
}

func TestSliceCleanupMissingComp(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))
}

func TestSliceCleanupStaleCache(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	noCacheClient := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: noCacheClient}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	noCacheComp := comp.DeepCopy()
	noCacheComp.ResourceVersion = ""
	require.NoError(t, noCacheClient.Create(ctx, noCacheComp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	// Cache would cause deletion, not actual
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "mismatch"}
	require.NoError(t, cli.Status().Update(ctx, comp))

	noCacheComp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: slice.Spec.SynthesisUUID}
	require.NoError(t, noCacheClient.Status().Update(ctx, noCacheComp))

	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Actual would cause deletion, not cache
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: slice.Spec.SynthesisUUID}
	require.NoError(t, cli.Status().Update(ctx, comp))

	noCacheComp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "mismatch"}
	require.NoError(t, noCacheClient.Status().Update(ctx, noCacheComp))

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Actual and cache would cause deletion
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "mismatch"}
	require.NoError(t, cli.Status().Update(ctx, comp))

	noCacheComp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "mismatch"}
	require.NoError(t, noCacheClient.Status().Update(ctx, noCacheComp))

	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))
}

func TestSliceCleanupSliceTooNew(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.CreationTimestamp = metav1.Now()
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	// Slice would have been deleted
	result, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	assert.NotZero(t, result.RequeueAfter)
}

func TestSliceCleanupFinalizersCompMissing(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.Finalizers = []string{"anything.io/any-finalizer"}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Delete(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}}}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// Comp exists - finalizer is not removed
	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Comp is gone - finalizer should be removed
	require.NoError(t, cli.Delete(ctx, comp))
	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))

	// Idempotence check (just make sure it doesn't error or panic)
	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
}

func TestSliceCleanupFinalizersCompReconciled(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := cleanupController{client: cli, noCacheReader: cli}

	comp := &apiv1.Composition{}
	comp.Name = "test-1"
	comp.Namespace = "default"
	require.NoError(t, cli.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-1"
	slice.Namespace = comp.Namespace
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.Finalizers = []string{"anything.io/any-finalizer"}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, cli.Scheme()))
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Delete(ctx, slice))
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}}}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// Comp exists - finalizer is not removed
	_, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))

	// Comp has been reconciled - finalizer should be removed
	comp.Status.CurrentSynthesis.Reconciled = ptr.To(metav1.Now())
	require.NoError(t, cli.Status().Update(ctx, comp))
	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	require.True(t, errors.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)))
}
