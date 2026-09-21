package resourceslice

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func newPendingRecoveryCleanup(t *testing.T) (client.Client, *apiv1.Composition, *apiv1.ResourceSlice) {
	t.Helper()
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "recovering", Namespace: "default", UID: "composition-uid",
			Finalizers: []string{"eno.azure.io/cleanup"},
		},
		Status: apiv1.CompositionStatus{
			CurrentSynthesis: &apiv1.Synthesis{
				UUID: "current", Synthesized: ptr.To(metav1.Now()), TombstoneRecoveryRequired: true,
			},
		},
	}
	slice := &apiv1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "overflow", Namespace: comp.Namespace,
			Labels:            map[string]string{apiv1.TombstoneRecoveryLabelKey: "true"},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute)),
			Finalizers:        []string{"eno.azure.io/cleanup"},
			OwnerReferences:   []metav1.OwnerReference{*metav1.NewControllerRef(comp, apiv1.SchemeGroupVersion.WithKind("Composition"))},
		},
		Spec: apiv1.ResourceSliceSpec{SynthesisUUID: comp.Status.CurrentSynthesis.UUID},
	}
	cli := testutil.NewClient(t, comp, slice)
	require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(comp), comp))
	return cli, comp, slice
}

func TestSliceCleanupRecoveryProtection(t *testing.T) {
	for _, tc := range []struct {
		name         string
		update       func(*apiv1.Composition)
		keep         bool
		requeueAfter time.Duration
	}{
		{name: "missing decision", keep: true, requeueAfter: 5 * time.Second},
		{
			name: "unfinished decision", keep: true, requeueAfter: 5 * time.Second,
			update: func(comp *apiv1.Composition) {
				comp.Status.CurrentSynthesis.TombstoneRecoveryFinished = &apiv1.TombstoneRecoveryStatus{
					SynthesisUUID: "current", Status: false,
				}
			},
		},
		{
			name: "stale completed decision", keep: true, requeueAfter: 5 * time.Second,
			update: func(comp *apiv1.Composition) {
				comp.Status.CurrentSynthesis.TombstoneRecoveryFinished = &apiv1.TombstoneRecoveryStatus{
					SynthesisUUID: "previous", Status: true,
				}
			},
		},
		{
			name: "recovery not required",
			update: func(comp *apiv1.Composition) {
				comp.Status.CurrentSynthesis.TombstoneRecoveryRequired = false
			},
		},
		{
			name: "no current synthesis",
			update: func(comp *apiv1.Composition) {
				comp.Status.CurrentSynthesis = nil
			},
		},
		{
			name: "current reference", keep: true,
			update: func(comp *apiv1.Composition) {
				comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{Name: "overflow"}}
			},
		},
		{
			name: "previous reference", keep: true,
			update: func(comp *apiv1.Composition) {
				comp.Status.PreviousSynthesis = &apiv1.Synthesis{
					ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "overflow"}},
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			cli, comp, slice := newPendingRecoveryCleanup(t)
			if tc.update != nil {
				tc.update(comp)
				require.NoError(t, cli.Status().Update(ctx, comp))
			}
			c := cleanupController{client: cli, noCacheReader: cli}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

			result, err := c.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.Equal(t, tc.requeueAfter, result.RequeueAfter)
			require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
			if tc.keep {
				assert.Nil(t, slice.DeletionTimestamp)
				assert.Equal(t, []string{"eno.azure.io/cleanup"}, slice.Finalizers)
			} else {
				require.NotNil(t, slice.DeletionTimestamp)
				_, err = c.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.True(t, errors.IsNotFound(cli.Get(ctx, req.NamespacedName, slice)))
			}
		})
	}
}

func TestSliceCleanupUnmarkedRecovery(t *testing.T) {
	for _, marker := range []string{"absent", "false"} {
		t.Run(marker, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			cli, _, slice := newPendingRecoveryCleanup(t)
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
			if marker == "absent" {
				slice.Labels = nil
			} else {
				slice.Labels[apiv1.TombstoneRecoveryLabelKey] = marker
			}
			require.NoError(t, cli.Update(ctx, slice))
			c := cleanupController{client: cli, noCacheReader: cli}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

			result, err := c.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.Zero(t, result.RequeueAfter)
			require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
			require.NotNil(t, slice.DeletionTimestamp, "unfinished recovery must not retain ordinary abandoned slices")
			_, err = c.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.True(t, errors.IsNotFound(cli.Get(ctx, req.NamespacedName, slice)))
		})
	}
}

func TestSliceCleanupRecoveryTransitions(t *testing.T) {
	for _, transition := range []string{"publish references", "finish without references", "supersede", "delete composition"} {
		t.Run(transition, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			cli, comp, slice := newPendingRecoveryCleanup(t)
			c := cleanupController{client: cli, noCacheReader: cli}
			req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

			// A failed publication leaves the same recovery pending across cleanup retries.
			for range 3 {
				result, err := c.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, 5*time.Second, result.RequeueAfter)
				require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
				require.Nil(t, slice.DeletionTimestamp)
			}

			switch transition {
			case "publish references", "finish without references":
				comp.Status.CurrentSynthesis.TombstoneRecoveryFinished = &apiv1.TombstoneRecoveryStatus{
					SynthesisUUID: "current", Status: true,
				}
				if transition == "publish references" {
					comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{Name: slice.Name}}
				}
				require.NoError(t, cli.Status().Update(ctx, comp))
			case "supersede":
				comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
				comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "next", TombstoneRecoveryRequired: true}
				require.NoError(t, cli.Status().Update(ctx, comp))
			case "delete composition":
				require.NoError(t, cli.Delete(ctx, comp))
			}

			result, err := c.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.Zero(t, result.RequeueAfter)
			require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
			if transition == "publish references" {
				assert.Nil(t, slice.DeletionTimestamp)
				return
			}
			require.NotNil(t, slice.DeletionTimestamp)
			_, err = c.Reconcile(ctx, req)
			require.NoError(t, err)
			assert.True(t, errors.IsNotFound(cli.Get(ctx, req.NamespacedName, slice)))
		})
	}
}

func TestSliceCleanupRecoveryStaleCache(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli, cachedComp, slice := newPendingRecoveryCleanup(t)
	liveComp := cachedComp.DeepCopy()
	liveClient := testutil.NewClient(t, liveComp)
	c := cleanupController{client: cli, noCacheReader: liveClient}
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(slice)}

	cachedComp.Status.CurrentSynthesis.TombstoneRecoveryRequired = false
	require.NoError(t, cli.Status().Update(ctx, cachedComp))
	result, err := c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "live pending recovery must prevent deletion and schedule a retry")
	require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
	require.Nil(t, slice.DeletionTimestamp)

	cachedComp.Status.CurrentSynthesis.TombstoneRecoveryRequired = true
	require.NoError(t, cli.Status().Update(ctx, cachedComp))
	liveComp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "next", TombstoneRecoveryRequired: true}
	require.NoError(t, liveClient.Status().Update(ctx, liveComp))
	result, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, result.RequeueAfter, "stale pending recovery must schedule another check")
	require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
	require.Nil(t, slice.DeletionTimestamp)

	cachedComp.Status = *liveComp.Status.DeepCopy()
	require.NoError(t, cli.Status().Update(ctx, cachedComp))
	result, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
	require.NoError(t, cli.Get(ctx, req.NamespacedName, slice))
	require.NotNil(t, slice.DeletionTimestamp)
	_, err = c.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.True(t, errors.IsNotFound(cli.Get(ctx, req.NamespacedName, slice)))
}

func TestSliceCleanupSliceReferences(t *testing.T) {
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
