package resourceslice

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/eno/internal/testutil"
	"github.com/Azure/eno/internal/testutil/statespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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

func TestMissingNewSlice(t *testing.T) {
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
	assert.False(t, comp.ShouldForceResynthesis())
}

func TestMissingOldSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
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
	assert.True(t, comp.ShouldForceResynthesis())

	// Check idempotence
	_, err = a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.True(t, comp.ShouldForceResynthesis())
}

func TestMissingOldSliceIgnoreSideEffects(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "nawr"}},
	}
	comp.EnableIgnoreSideEffects()
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Nil(t, comp.Status.CurrentSynthesis.Ready)
	assert.Nil(t, comp.Status.CurrentSynthesis.Reconciled)
	assert.False(t, comp.ShouldForceResynthesis())
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
	assert.False(t, comp.ShouldForceResynthesis())
}

func TestMissingSliceStaleCache(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))),
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "slice"}},
	}

	slice := &apiv1.ResourceSlice{}
	slice.Name = "slice"
	slice.Namespace = comp.Namespace
	require.NoError(t, cli.Create(ctx, slice))

	a := &sliceController{client: cli}
	_, err := a.handleMissingSlice(ctx, comp, slice.Name)
	require.NoError(t, err) // this would error on update since the composition doesn't exist
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

func TestFuzzProcessCompositionTransition(t *testing.T) {
	statespace.Test(func(test *compositionTransitionTest) bool {
		return processCompositionTransition(context.Background(), test.Composition.DeepCopy(), test.Snapshot)
	}).
		WithInitialState(func() *compositionTransitionTest {
			return &compositionTransitionTest{
				Composition: &apiv1.Composition{},
			}
		}).
		WithMutation("has in-flight synthesis", func(c *compositionTransitionTest) *compositionTransitionTest {
			c.Composition.Status.InFlightSynthesis = &apiv1.Synthesis{}
			return c
		}).
		WithMutation("has current synthesis", func(c *compositionTransitionTest) *compositionTransitionTest {
			c.Composition.Status.CurrentSynthesis = &apiv1.Synthesis{}
			return c
		}).
		WithMutation("has previous synthesis", func(c *compositionTransitionTest) *compositionTransitionTest {
			c.Composition.Status.PreviousSynthesis = &apiv1.Synthesis{}
			return c
		}).
		WithMutation("current synthesis is ready", func(c *compositionTransitionTest) *compositionTransitionTest {
			if c.Composition.Status.CurrentSynthesis != nil {
				c.Composition.Status.CurrentSynthesis.Ready = &metav1.Time{}
			}
			return c
		}).
		WithMutation("current synthesis is reconciled", func(c *compositionTransitionTest) *compositionTransitionTest {
			if c.Composition.Status.CurrentSynthesis != nil {
				c.Composition.Status.CurrentSynthesis.Reconciled = &metav1.Time{}
			}
			return c
		}).
		WithMutation("current synthesis has a resource slice", func(c *compositionTransitionTest) *compositionTransitionTest {
			if c.Composition.Status.CurrentSynthesis != nil {
				c.Composition.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{Name: "test"}}
			}
			return c
		}).
		WithMutation("snapshot is reconciled", func(c *compositionTransitionTest) *compositionTransitionTest {
			c.Snapshot.Ready = true
			return c
		}).
		WithMutation("snapshot is ready", func(c *compositionTransitionTest) *compositionTransitionTest {
			c.Snapshot.Ready = true
			return c
		}).
		WithMutation("snapshot has ready timestamp", func(c *compositionTransitionTest) *compositionTransitionTest {
			c.Snapshot.ReadyTime = &metav1.Time{}
			return c
		}).
		WithInvariant("modified when state has transitioned", func(state *compositionTransitionTest, result bool) bool {
			syn := state.Composition.Status.CurrentSynthesis
			if syn == nil {
				return true
			}
			readinessTransition := state.Snapshot.Ready != (syn.Ready != nil)
			reconciledTransition := state.Snapshot.Reconciled != (syn.Reconciled != nil)
			return result == (readinessTransition || reconciledTransition)
		}).
		Evaluate(t)
}

type compositionTransitionTest struct {
	Composition *apiv1.Composition
	Snapshot    statusSnapshot
}
