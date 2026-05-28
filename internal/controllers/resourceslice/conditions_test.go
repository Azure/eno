package resourceslice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

// --- U1: All-healthy snapshot seeds both conditions True with empty message ---
func TestProcessTransition_AllHealthy_SeedsConditions(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Generation = 7
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ObservedCompositionGeneration: 7,
	}
	snapshot := statusSnapshot{Reconciled: true, Ready: true}

	modified := processCompositionTransition(context.Background(), comp, snapshot)
	require.True(t, modified)

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, metav1.ConditionTrue, applied.Status)
	assert.Equal(t, reasonAllResourcesHealthy, applied.Reason)
	assert.Empty(t, applied.Message)
	assert.Equal(t, int64(7), applied.ObservedGeneration)

	ready := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, reasonAllResourcesHealthy, ready.Reason)
	assert.Empty(t, ready.Message)
}

// --- U2: NotApplied snapshot builds correct False/Message ---
func TestProcessTransition_NotApplied_BuildsMessage(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
	snapshot := statusSnapshot{
		Reconciled: false,
		Ready:      true,
		NotApplied: []string{"Deployment/foo", "Service/bar"},
	}

	require.True(t, processCompositionTransition(context.Background(), comp, snapshot))

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, metav1.ConditionFalse, applied.Status)
	assert.Equal(t, reasonNotAllApplied, applied.Reason)
	assert.Equal(t, "Deployment/foo, Service/bar", applied.Message)
}

// --- U3: NotReady snapshot builds correct False/Message ---
func TestProcessTransition_NotReady_BuildsMessage(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
	snapshot := statusSnapshot{
		Reconciled: true,
		Ready:      false,
		NotReady:   []string{"StatefulSet/db"},
	}

	require.True(t, processCompositionTransition(context.Background(), comp, snapshot))

	ready := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, reasonNotAllReady, ready.Reason)
	assert.Equal(t, "StatefulSet/db", ready.Message)
}

// --- U4: Overflow appended after the cap ---
func TestProcessTransition_OverflowAppended(t *testing.T) {
	sample := make([]string, resourcesCap)
	for i := 0; i < resourcesCap; i++ {
		sample[i] = fmt.Sprintf("Kind%02d/name", i)
	}
	comp := &apiv1.Composition{}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
	snapshot := statusSnapshot{
		Reconciled:      false,
		Ready:           true,
		NotApplied:      sample,
		OverflowApplied: 25,
	}

	require.True(t, processCompositionTransition(context.Background(), comp, snapshot))

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.True(t, strings.HasSuffix(applied.Message, ", +25 more"), "message=%q", applied.Message)
}

// --- U5: formatBlockingMessages table test ---
func TestFormatBlockingMessages(t *testing.T) {
	tests := []struct {
		name     string
		sample   []string
		overflow int
		want     string
	}{
		{"empty", nil, 0, ""},
		{"single", []string{"a"}, 0, "a"},
		{"multiple", []string{"a", "b"}, 0, "a, b"},
		{"with overflow", []string{"a", "b"}, 3, "a, b, +3 more"},
		{"empty sample with overflow", nil, 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatBlockingMessages(tt.sample, tt.overflow))
		})
	}
}

// --- U6: conditionMatches table test ---
func TestConditionMatches(t *testing.T) {
	withCond := func(status metav1.ConditionStatus, msg string) *apiv1.Composition {
		c := &apiv1.Composition{}
		c.Status.CurrentSynthesis = &apiv1.Synthesis{
			Conditions: []metav1.Condition{
				{Type: apiv1.ConditionResourcesApplied, Status: status, Message: msg},
			},
		}
		return c
	}

	tests := []struct {
		name     string
		comp     *apiv1.Composition
		wantTrue bool
		wantMsg  string
		want     bool
	}{
		{"nil CurrentSynthesis", &apiv1.Composition{}, true, "", true},
		{"missing condition", &apiv1.Composition{Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{}}}, true, "", false},
		{"true matches", withCond(metav1.ConditionTrue, ""), true, "", true},
		{"true mismatches false", withCond(metav1.ConditionTrue, ""), false, "", false},
		{"false matches false", withCond(metav1.ConditionFalse, "x"), false, "x", true},
		{"false matches false but msg differs", withCond(metav1.ConditionFalse, "x"), false, "y", false},
		{"false-with-empty distinct from true-with-empty", withCond(metav1.ConditionTrue, ""), false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, conditionMatches(tt.comp, apiv1.ConditionResourcesApplied, tt.wantTrue, tt.wantMsg))
		})
	}
}

// --- U7: First reconcile seeds conditions on a healthy pre-upgrade composition ---
func TestProcessTransition_SeedsOnFirstReconcile(t *testing.T) {
	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Reconciled: &now,
		Ready:      &now,
		// Conditions intentionally nil (pre-upgrade state).
	}
	snapshot := statusSnapshot{Reconciled: true, Ready: true, ReadyTime: &now}

	require.True(t, processCompositionTransition(context.Background(), comp, snapshot), "first reconcile must seed conditions")
	require.NotNil(t, meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied))
	require.NotNil(t, meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady))

	// Second call with same snapshot is a no-op.
	assert.False(t, processCompositionTransition(context.Background(), comp, snapshot), "second reconcile must short-circuit")
}

// --- U8: False with empty message is distinct from True with empty message ---
func TestProcessTransition_FalseEmptyMessageDistinct(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		// pre-existing True condition with empty message
		Conditions: []metav1.Condition{{
			Type:   apiv1.ConditionResourcesApplied,
			Status: metav1.ConditionTrue,
		}},
	}
	// Snapshot says NotReconciled but produces no parseable identifiers.
	snapshot := statusSnapshot{Reconciled: false, Ready: true}

	require.True(t, processCompositionTransition(context.Background(), comp, snapshot))
	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, metav1.ConditionFalse, applied.Status)
	assert.Empty(t, applied.Message)
}

// --- U9: Empty composition (no slices) seeds both conditions True ---
func TestProcessTransition_EmptyComposition(t *testing.T) {
	comp := &apiv1.Composition{}
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ResourceSlices: nil,
	}
	snapshot := statusSnapshot{Reconciled: true, Ready: true}

	require.True(t, processCompositionTransition(context.Background(), comp, snapshot))
	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	ready := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady)
	require.NotNil(t, applied)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, applied.Status)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
}

// --- shared helpers for R-tests ---

func mustManifest(t *testing.T, kind, name string) apiv1.Manifest {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"kind":     kind,
		"metadata": map[string]any{"name": name},
	})
	require.NoError(t, err)
	return apiv1.Manifest{Manifest: string(b)}
}

type writeCounter struct {
	statusUpdates int64
}

func (w *writeCounter) reset() { atomic.StoreInt64(&w.statusUpdates, 0) }
func (w *writeCounter) count() int64 {
	return atomic.LoadInt64(&w.statusUpdates)
}

func newCountingClient(t *testing.T, w *writeCounter, objs ...client.Object) client.Client {
	return testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if sub == "status" {
				if _, ok := obj.(*apiv1.Composition); ok {
					atomic.AddInt64(&w.statusUpdates, 1)
				}
			}
			return c.Status().Update(ctx, obj, opts...)
		},
	}, objs...)
}

// reconcileCompAndSlices is a small helper to set up a composition with one slice
// containing the given manifests/states, run a single Reconcile pass, and return
// the fetched composition.
func reconcileWithSlice(t *testing.T, ctx context.Context, cli client.Client, manifests []apiv1.Manifest, states []apiv1.ResourceState) *apiv1.Composition {
	t.Helper()
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.Spec.Resources = manifests
	slice.Status.Resources = states
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
	return comp
}

// --- R1: NotApplied sample populated from slice manifests ---
func TestAggregation_PopulatesNotAppliedSample(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	manifests := []apiv1.Manifest{
		mustManifest(t, "Deployment", "foo"),
		mustManifest(t, "Service", "bar"),
	}
	states := []apiv1.ResourceState{
		{Reconciled: false},
		{Reconciled: true, Ready: &now},
	}
	comp := reconcileWithSlice(t, ctx, cli, manifests, states)

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, metav1.ConditionFalse, applied.Status)
	assert.Equal(t, "Deployment/foo", applied.Message)
}

// --- R2: NotReady sample populated ---
func TestAggregation_PopulatesNotReadySample(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	now := metav1.Now()
	manifests := []apiv1.Manifest{
		mustManifest(t, "Deployment", "foo"),
		mustManifest(t, "Service", "bar"),
	}
	states := []apiv1.ResourceState{
		{Reconciled: true, Ready: nil},
		{Reconciled: true, Ready: &now},
	}
	comp := reconcileWithSlice(t, ctx, cli, manifests, states)

	ready := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, "Deployment/foo", ready.Message)
}

// --- R3: Missing status entries treated as not-reconciled / not-ready ---
func TestAggregation_MissingStatusEntries(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	manifests := []apiv1.Manifest{
		mustManifest(t, "Deployment", "foo"),
		mustManifest(t, "Service", "bar"),
		mustManifest(t, "ConfigMap", "baz"),
	}
	comp := reconcileWithSlice(t, ctx, cli, manifests, nil) // status entirely absent

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	ready := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady)
	require.NotNil(t, applied)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, applied.Status)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	// Sorted alphabetically.
	assert.Equal(t, "ConfigMap/baz, Deployment/foo, Service/bar", applied.Message)
	assert.Equal(t, "ConfigMap/baz, Deployment/foo, Service/bar", ready.Message)
}

// --- R4: Sort stability across reconciles (P0 regression test) ---
func TestAggregation_SortStability(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	// Manifests intentionally out of alphabetical order.
	manifests := []apiv1.Manifest{
		mustManifest(t, "Service", "zeta"),
		mustManifest(t, "ConfigMap", "alpha"),
		mustManifest(t, "Deployment", "mike"),
	}
	comp := reconcileWithSlice(t, ctx, cli, manifests, nil)

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	want := "ConfigMap/alpha, Deployment/mike, Service/zeta"
	assert.Equal(t, want, applied.Message, "must be alphabetically sorted")
	firstTransition := applied.LastTransitionTime

	// Reconcile again with no input changes — message must not move, LastTransitionTime must not advance.
	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}
	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	applied = meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, want, applied.Message)
	assert.Equal(t, firstTransition, applied.LastTransitionTime, "LastTransitionTime must be stable across no-op reconciles")
}

// --- R5: cap=25 with +N more overflow rendered end-to-end ---
func TestAggregation_Cap25Overflow(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	const n = 50
	manifests := make([]apiv1.Manifest, n)
	states := make([]apiv1.ResourceState, n)
	for i := 0; i < n; i++ {
		// Pad to 2 digits so alphabetical sort matches numerical order.
		manifests[i] = mustManifest(t, "Kind", fmt.Sprintf("res-%02d", i))
		states[i] = apiv1.ResourceState{Reconciled: false}
	}
	comp := reconcileWithSlice(t, ctx, cli, manifests, states)

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	parts := strings.Split(applied.Message, ", ")
	assert.Equal(t, resourcesCap+1, len(parts), "expected 25 entries plus the overflow suffix")
	assert.Equal(t, fmt.Sprintf("+%d more", n-resourcesCap), parts[len(parts)-1])
}

// --- R6: Unparseable manifest is silently dropped from the sample ---
func TestAggregation_UnparseableManifest(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	manifests := []apiv1.Manifest{
		{Manifest: `{"not json`},
		mustManifest(t, "Deployment", "foo"),
	}
	states := []apiv1.ResourceState{{Reconciled: false}, {Reconciled: false}}
	comp := reconcileWithSlice(t, ctx, cli, manifests, states)

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, metav1.ConditionFalse, applied.Status)
	assert.Equal(t, "Deployment/foo", applied.Message, "garbage entry must be omitted")
}

// --- R7: Patch CR surfaces under its own identity ---
func TestAggregation_PatchCRIdentifier(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	manifests := []apiv1.Manifest{
		{Manifest: `{"kind":"Patch","metadata":{"name":"p1"},"patch":{"apiVersion":"v1","kind":"ConfigMap","name":"target"}}`},
	}
	states := []apiv1.ResourceState{{Reconciled: false}}
	comp := reconcileWithSlice(t, ctx, cli, manifests, states)

	applied := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied)
	require.NotNil(t, applied)
	assert.Equal(t, "Patch/p1", applied.Message)
}

// --- R8: First reconcile seeds conditions; second reconcile is a no-op (zero writes) ---
func TestAggregation_FirstReconcileSeedsThenIdempotent(t *testing.T) {
	ctx := testutil.NewContext(t)
	w := &writeCounter{}
	cli := newCountingClient(t, w)

	now := metav1.Now()
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.Spec.Resources = []apiv1.Manifest{mustManifest(t, "Deployment", "foo")}
	slice.Status.Resources = []apiv1.ResourceState{{Reconciled: true, Ready: &now}}
	require.NoError(t, cli.Create(ctx, slice))
	require.NoError(t, cli.Status().Update(ctx, slice))

	comp := &apiv1.Composition{}
	comp.Name = "test"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		Synthesized:    &now,
		Reconciled:     &now, // pre-upgrade healthy state
		Ready:          &now,
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
		// Conditions intentionally nil.
	}
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp))
	w.reset() // ignore the bootstrap write above

	a := &sliceController{client: cli}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}}

	_, err := a.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int64(1), w.count(), "first reconcile must seed the conditions")

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.NotNil(t, meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesApplied))
	require.NotNil(t, meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, apiv1.ConditionResourcesReady))

	_, err = a.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int64(1), w.count(), "second reconcile must not write")
}
