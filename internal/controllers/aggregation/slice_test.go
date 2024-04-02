package aggregation

import (
	"testing"
	"time"

	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

func testAggregation(t *testing.T, ready bool, reconciled bool) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	var readyTime *metav1.Time
	if ready {
		now := metav1.Now()
		readyTime = &now
	}

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: readyTime, Reconciled: reconciled}}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Equal(t, reconciled, comp.Status.CurrentSynthesis.Reconciled != nil)
	assert.Equal(t, ready, comp.Status.CurrentSynthesis.Ready != nil)
}

func TestAggregationHappyPath(t *testing.T) {
	testAggregation(t, true, true)
}

func TestAggregationNegative(t *testing.T) {
	testAggregation(t, false, false)
}

func TestAggregationReadyNotReconciled(t *testing.T) {
	testAggregation(t, true, false)
}

func TestAggregationReconciledNotReady(t *testing.T) {
	testAggregation(t, false, true)
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

	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Reconciled)
	assert.Nil(t, comp.Status.CurrentSynthesis.Ready)
}

func TestCleanupSafety(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{{Ready: &now, Reconciled: true}}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Finalizers = []string{"test"}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))
	require.NoError(t, cli.Delete(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Reconciled)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Ready)
}

func TestReadyTimeAggregation(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	latestReadyTime := metav1.NewTime(now.Add(time.Hour))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}"}, {Manifest: "{}"}}
	slice.Status.Resources = []apiv1.ResourceState{
		{Ready: &latestReadyTime, Reconciled: true},
		{Ready: &now, Reconciled: true},
	}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	// The max latest time is taken even though it was listed before the others
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Equal(t, latestReadyTime.Round(time.Minute), comp.Status.CurrentSynthesis.Ready.Round(time.Minute))
}

func TestNoSlices(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Ready)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Reconciled)
}

func TestMissingSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "nawr"}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Ready)
	assert.Nil(t, comp.Status.CurrentSynthesis.Reconciled)
}

func TestMissingSliceWhileDeleting(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Finalizers = []string{"anything"}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "nawr"}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))
	require.NoError(t, cli.Delete(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Ready)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Reconciled)
}

func TestOrphanedOnPurpose(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{{Manifest: "{}", Deleted: true}}
	slice.Status.Resources = []apiv1.ResourceState{{Reconciled: true}}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Annotations = map[string]string{"eno.azure.io/deletion-strategy": "orphan"}
	comp.Finalizers = []string{"anything"}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))
	require.NoError(t, cli.Delete(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Ready)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Reconciled)
}
