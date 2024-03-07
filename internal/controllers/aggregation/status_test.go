package aggregation

import (
	"testing"

	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

func testAggregation(t *testing.T, ready *bool, reconciled bool) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: ready, Reconciled: reconciled}}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    true,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &statusController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Equal(t, reconciled, comp.Status.CurrentSynthesis.Reconciled)
	if ready == nil {
		assert.False(t, comp.Status.CurrentSynthesis.Ready)
	} else {
		assert.Equal(t, *ready, comp.Status.CurrentSynthesis.Ready)
	}
}

func TestAggregationHappyPath(t *testing.T) {
	ready := true
	testAggregation(t, &ready, true)
}

func TestAggregationNegative(t *testing.T) {
	ready := false
	testAggregation(t, &ready, false)
}

func TestAggregationReadinessUnknown(t *testing.T) {
	testAggregation(t, nil, false)
}

func TestAggregationReadyNotReconciled(t *testing.T) {
	ready := true
	testAggregation(t, &ready, false)
}

func TestAggregationReconciledNotReady(t *testing.T) {
	ready := false
	testAggregation(t, &ready, true)
}

func TestStaleStatus(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = nil // status hasn't been populated yet
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    true,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &statusController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.False(t, comp.Status.CurrentSynthesis.Reconciled)
	assert.False(t, comp.Status.CurrentSynthesis.Ready)
}

func TestCleanupSafety(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	ready := true
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: &ready, Reconciled: true}}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Finalizers = []string{"test"}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    true,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))
	require.NoError(t, cli.Delete(ctx, comp))

	a := &statusController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.False(t, comp.Status.CurrentSynthesis.Reconciled)
	assert.True(t, comp.Status.CurrentSynthesis.Ready)
}
