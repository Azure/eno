package symphony

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func compWithConditions(name string, generation int64, applied, ready *metav1.Condition) apiv1.Composition {
	var conds []metav1.Condition
	if applied != nil {
		conds = append(conds, *applied)
	}
	if ready != nil {
		conds = append(conds, *ready)
	}
	return apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: generation},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: name + "-synth"}},
		Status: apiv1.CompositionStatus{
			CurrentSynthesis: &apiv1.Synthesis{
				ObservedCompositionGeneration: generation,
				Conditions:                    conds,
			},
		},
	}
}

func condTrue(t string) *metav1.Condition {
	return &metav1.Condition{Type: t, Status: metav1.ConditionTrue}
}

func condFalse(t, msg string) *metav1.Condition {
	return &metav1.Condition{Type: t, Status: metav1.ConditionFalse, Message: msg}
}

func symphonyWith(synths ...string) *apiv1.Symphony {
	s := &apiv1.Symphony{}
	for _, n := range synths {
		s.Spec.Variations = append(s.Spec.Variations, apiv1.Variation{Synthesizer: apiv1.SynthesizerRef{Name: n + "-synth"}})
	}
	return s
}

// --- S1: all healthy → no blockers ---
func TestBuildStatus_AllHealthy(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a", "b", "c")
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
		compWithConditions("b", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
		compWithConditions("c", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
	}}

	_, blockers := c.buildStatus(symph, comps)
	assert.Empty(t, blockers)
}

// --- S2: applied blocker is captured with message ---
func TestBuildStatus_AppliedBlocker(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a")
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1,
			condFalse(apiv1.ConditionResourcesApplied, "Deployment/foo, Service/bar"),
			condTrue(apiv1.ConditionResourcesReady)),
	}}

	_, blockers := c.buildStatus(symph, comps)
	require.Len(t, blockers, 1)
	assert.True(t, blockers[0].notApplied)
	assert.False(t, blockers[0].notReady)
	assert.Equal(t, "Deployment/foo, Service/bar", blockers[0].appliedMsg)
	assert.Equal(t, "NotApplied: a [Deployment/foo, Service/bar]", formatSymphonyMessage(blockers))
}

// --- S3: ready blocker is captured with message ---
func TestBuildStatus_ReadyBlocker(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a")
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1,
			condTrue(apiv1.ConditionResourcesApplied),
			condFalse(apiv1.ConditionResourcesReady, "StatefulSet/db")),
	}}

	_, blockers := c.buildStatus(symph, comps)
	require.Len(t, blockers, 1)
	assert.False(t, blockers[0].notApplied)
	assert.True(t, blockers[0].notReady)
	assert.Equal(t, "StatefulSet/db", blockers[0].readyMsg)
	assert.Equal(t, "NotReady: a [StatefulSet/db]", formatSymphonyMessage(blockers))
}

// --- S4: both sections, sorted by composition name ---
func TestBuildStatus_BothSectionsSorted(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a", "b", "c")
	// Pass items out of alphabetical order to verify the sort.
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("b", 1,
			condTrue(apiv1.ConditionResourcesApplied),
			condFalse(apiv1.ConditionResourcesReady, "StatefulSet/db")),
		compWithConditions("a", 1,
			condFalse(apiv1.ConditionResourcesApplied, "Deployment/foo, Service/bar"),
			condTrue(apiv1.ConditionResourcesReady)),
		compWithConditions("c", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
	}}

	_, blockers := c.buildStatus(symph, comps)
	require.Len(t, blockers, 2)
	assert.Equal(t, "a", blockers[0].name)
	assert.Equal(t, "b", blockers[1].name)

	want := "NotApplied: a [Deployment/foo, Service/bar]\nNotReady: b [StatefulSet/db]"
	assert.Equal(t, want, formatSymphonyMessage(blockers))
}

// --- S5: optional variation is ignored ---
func TestBuildStatus_OptionalIgnored(t *testing.T) {
	c := &symphonyController{}
	symph := &apiv1.Symphony{
		Spec: apiv1.SymphonySpec{
			Variations: []apiv1.Variation{
				{Synthesizer: apiv1.SynthesizerRef{Name: "a-synth"}},
				{Synthesizer: apiv1.SynthesizerRef{Name: "b-synth"}, Optional: true},
			},
		},
	}
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
		compWithConditions("b", 1,
			condFalse(apiv1.ConditionResourcesApplied, "Deployment/broken"),
			condFalse(apiv1.ConditionResourcesReady, "Deployment/broken")),
	}}

	_, blockers := c.buildStatus(symph, comps)
	assert.Empty(t, blockers, "optional broken composition must not appear in blockers")
}

// --- S6: synInvalid (stale observedGeneration) produces empty-bracketed entries ---
func TestBuildStatus_SynInvalidStaleGeneration(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a")
	comp := compWithConditions("a", 5, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady))
	comp.Status.CurrentSynthesis.ObservedCompositionGeneration = 4 // stale
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{comp}}

	_, blockers := c.buildStatus(symph, comps)
	require.Len(t, blockers, 1)
	assert.True(t, blockers[0].notApplied)
	assert.True(t, blockers[0].notReady)
	assert.Empty(t, blockers[0].appliedMsg)
	assert.Empty(t, blockers[0].readyMsg)

	assert.Equal(t, "NotApplied: a []\nNotReady: a []", formatSymphonyMessage(blockers))
}

// --- S7: deletion timestamp produces synInvalid behavior ---
func TestBuildStatus_SynInvalidDeletionTimestamp(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a")
	comp := compWithConditions("a", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady))
	now := metav1.Now()
	comp.DeletionTimestamp = &now
	comps := &apiv1.CompositionList{Items: []apiv1.Composition{comp}}

	_, blockers := c.buildStatus(symph, comps)
	require.Len(t, blockers, 1)
	assert.True(t, blockers[0].notApplied)
	assert.True(t, blockers[0].notReady)
}

// --- S8: sort stability across reordered inputs ---
func TestBuildStatus_SortStability(t *testing.T) {
	c := &symphonyController{}
	symph := symphonyWith("a", "b", "c")
	mkBroken := func(name string) apiv1.Composition {
		return compWithConditions(name, 1,
			condFalse(apiv1.ConditionResourcesApplied, "Kind/"+name),
			condTrue(apiv1.ConditionResourcesReady))
	}

	orderA := &apiv1.CompositionList{Items: []apiv1.Composition{mkBroken("a"), mkBroken("b"), mkBroken("c")}}
	orderB := &apiv1.CompositionList{Items: []apiv1.Composition{mkBroken("c"), mkBroken("a"), mkBroken("b")}}

	_, blockersA := c.buildStatus(symph, orderA)
	_, blockersB := c.buildStatus(symph, orderB)

	require.Equal(t, len(blockersA), len(blockersB))
	for i := range blockersA {
		assert.Equal(t, blockersA[i].name, blockersB[i].name, "blocker order must be deterministic")
	}
	assert.Equal(t, formatSymphonyMessage(blockersA), formatSymphonyMessage(blockersB))
}

// --- S9: formatSymphonyMessage with no blockers returns empty string ---
func TestFormatSymphonyMessage_Empty(t *testing.T) {
	assert.Empty(t, formatSymphonyMessage(nil))
	assert.Empty(t, formatSymphonyMessage([]symphonyConditions{}))
}

// --- S10: formatSymphonyMessage with only NotApplied section has no NotReady line ---
func TestFormatSymphonyMessage_SingleSection(t *testing.T) {
	out := formatSymphonyMessage([]symphonyConditions{{name: "a", notApplied: true, appliedMsg: "X/y"}})
	assert.Equal(t, "NotApplied: a [X/y]", out)
	assert.False(t, strings.Contains(out, "NotReady"))
	assert.False(t, strings.HasSuffix(out, "\n"))
}

// --- Helpers for SI-tests: counting fake client for status patches ---
type patchCounter struct{ patches int64 }

func (p *patchCounter) reset() { atomic.StoreInt64(&p.patches, 0) }
func (p *patchCounter) count() int64 {
	return atomic.LoadInt64(&p.patches)
}

func newSymphonyCountingClient(t *testing.T, p *patchCounter, objs ...client.Object) client.Client {
	return testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if sub == "status" {
				if _, ok := obj.(*apiv1.Symphony); ok {
					atomic.AddInt64(&p.patches, 1)
				}
			}
			return c.Status().Patch(ctx, obj, patch, opts...)
		},
	}, objs...)
}

// --- SI1: syncStatus is idempotent across no-op reconciles ---
func TestSyncStatus_Idempotency(t *testing.T) {
	ctx := testutil.NewContext(t)
	p := &patchCounter{}

	symph := &apiv1.Symphony{}
	symph.Name = "s"
	symph.Namespace = "default"
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "a-synth"}}}

	cli := newSymphonyCountingClient(t, p, symph)
	c := &symphonyController{client: cli}

	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
	}}

	require.NoError(t, c.syncStatus(ctx, symph, comps))
	assert.Equal(t, int64(1), p.count(), "first syncStatus must write to seed the condition")

	// Re-fetch the patched symphony so subsequent calls see the persisted state.
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(symph), symph))

	require.NoError(t, c.syncStatus(ctx, symph, comps))
	assert.Equal(t, int64(1), p.count(), "second syncStatus with no change must not write")
}

// --- SI2: end-to-end propagation of a child composition's blocker into the symphony condition ---
func TestSyncStatus_MessagePropagation(t *testing.T) {
	ctx := testutil.NewContext(t)

	symph := &apiv1.Symphony{}
	symph.Name = "s"
	symph.Namespace = "default"
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "a-synth"}}}

	cli := testutil.NewClient(t, symph)
	c := &symphonyController{client: cli}

	comps := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1,
			condFalse(apiv1.ConditionResourcesApplied, "Deployment/foo, Service/bar"),
			condTrue(apiv1.ConditionResourcesReady)),
	}}

	require.NoError(t, c.syncStatus(ctx, symph, comps))
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(symph), symph))

	cond := meta.FindStatusCondition(symph.Status.Conditions, apiv1.ConditionSymphonyReady)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, apiv1.NotAllCompositionsReadyReason, cond.Reason)
	assert.Equal(t, "NotApplied: a [Deployment/foo, Service/bar]", cond.Message)
}

// --- SI3: blocker clears → symphony condition flips True; LastTransitionTime advances exactly once ---
func TestSyncStatus_BlockerClears(t *testing.T) {
	ctx := testutil.NewContext(t)

	symph := &apiv1.Symphony{}
	symph.Name = "s"
	symph.Namespace = "default"
	symph.Spec.Variations = []apiv1.Variation{{Synthesizer: apiv1.SynthesizerRef{Name: "a-synth"}}}

	cli := testutil.NewClient(t, symph)
	c := &symphonyController{client: cli}

	broken := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1,
			condFalse(apiv1.ConditionResourcesApplied, "Deployment/foo"),
			condTrue(apiv1.ConditionResourcesReady)),
	}}
	require.NoError(t, c.syncStatus(ctx, symph, broken))
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(symph), symph))
	before := meta.FindStatusCondition(symph.Status.Conditions, apiv1.ConditionSymphonyReady)
	require.NotNil(t, before)
	require.Equal(t, metav1.ConditionFalse, before.Status)

	healthy := &apiv1.CompositionList{Items: []apiv1.Composition{
		compWithConditions("a", 1, condTrue(apiv1.ConditionResourcesApplied), condTrue(apiv1.ConditionResourcesReady)),
	}}
	require.NoError(t, c.syncStatus(ctx, symph, healthy))
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(symph), symph))
	after := meta.FindStatusCondition(symph.Status.Conditions, apiv1.ConditionSymphonyReady)
	require.NotNil(t, after)
	assert.Equal(t, metav1.ConditionTrue, after.Status)
	assert.Equal(t, apiv1.AllCompositionsReadyReason, after.Reason)
	assert.Empty(t, after.Message)
	assert.True(t, after.LastTransitionTime.After(before.LastTransitionTime.Time),
		"LastTransitionTime must strictly advance on a real status flip")
}
