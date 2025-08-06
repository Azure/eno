package scheduling

import (
	"encoding/json"
	"math/rand"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/Azure/eno/internal/testutil/statespace"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestFuzzNewOp(t *testing.T) {
	ctx := testutil.NewContext(t)

	statespace.Test(func(state newOpTestState) *op {
		return newOp(state.synth, state.comp, time.Time{})
	}).WithInitialState(func() newOpTestState {
		synth := &apiv1.Synthesizer{
			ObjectMeta: metav1.ObjectMeta{Name: "test-synth", Generation: 11},
			Spec: apiv1.SynthesizerSpec{
				Refs: []apiv1.Ref{
					{Key: "foo"},
					{Key: "bar", Defer: true},
				},
			},
		}

		initTS := time.Date(8000, 0, 0, 0, 0, 0, 0, time.UTC)
		comp := &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{Name: "test-comp", Finalizers: []string{"eno.azure.io/cleanup"}, Generation: 1},
			Spec: apiv1.CompositionSpec{
				Bindings: []apiv1.Binding{
					{Key: "foo", Resource: apiv1.ResourceBinding{Name: "foo"}},
					{Key: "bar", Resource: apiv1.ResourceBinding{Name: "bar"}},
				},
			},
			Status: apiv1.CompositionStatus{
				CurrentSynthesis: &apiv1.Synthesis{
					ObservedCompositionGeneration: 1,
					ObservedSynthesizerGeneration: 11,
					Synthesized:                   ptr.To(metav1.Now()),
					Initialized:                   ptr.To(metav1.NewTime(initTS)),
					UUID:                          "initial-uuid",
					InputRevisions: []apiv1.InputRevisions{
						{Key: "foo", ResourceVersion: "1"},
						{Key: "bar", ResourceVersion: "2"},
					},
				},
				InputRevisions: []apiv1.InputRevisions{
					{Key: "foo", ResourceVersion: "1"},
					{Key: "bar", ResourceVersion: "2"},
				},
			},
		}

		return newOpTestState{
			synth:    synth,
			comp:     comp.DeepCopy(),
			original: comp,
		}
	}).WithMutation("inputModified", func(state newOpTestState) newOpTestState {
		state.comp.Status.InputRevisions[0].ResourceVersion = "modified"
		return state
	}).WithMutation("deferredInputModified", func(state newOpTestState) newOpTestState {
		if len(state.comp.Status.InputRevisions) >= 2 {
			state.comp.Status.InputRevisions[1].ResourceVersion = "modified"
		}
		return state
	}).WithMutation("inputsMissing", func(state newOpTestState) newOpTestState {
		state.comp.Status.InputRevisions = state.comp.Status.InputRevisions[:1]
		return state
	}).WithMutation("inputsOutOfLockstep", func(state newOpTestState) newOpTestState {
		if len(state.comp.Status.InputRevisions) >= 2 {
			state.comp.Status.InputRevisions[0].Revision = ptr.To(123)
			state.comp.Status.InputRevisions[1].Revision = ptr.To(234)
		}
		return state
	}).WithMutation("ignoreSideEffects", func(state newOpTestState) newOpTestState {
		state.comp.EnableIgnoreSideEffects()
		return state
	}).WithMutation("missingFinalizer", func(state newOpTestState) newOpTestState {
		state.comp.Finalizers = nil
		return state
	}).WithMutation("synthModified", func(state newOpTestState) newOpTestState {
		state.synth.Generation = 234
		return state
	}).WithMutation("synthGenZero", func(state newOpTestState) newOpTestState {
		state.synth.Generation = 0
		return state
	}).WithMutation("forceResynth", func(state newOpTestState) newOpTestState {
		state.comp.ForceResynthesis()
		return state
	}).WithMutation("synthesizing", func(state newOpTestState) newOpTestState {
		state.comp.Status.InFlightSynthesis = state.comp.Status.CurrentSynthesis
		state.comp.Status.CurrentSynthesis = nil
		return state
	}).WithMutation("compDeleting", func(state newOpTestState) newOpTestState {
		state.comp.DeletionTimestamp = ptr.To(metav1.Now())
		return state
	}).WithMutation("compModified", func(state newOpTestState) newOpTestState {
		state.comp.Generation = 345
		return state
	}).WithMutation("nilSynthesis", func(state newOpTestState) newOpTestState {
		state.comp.Status.CurrentSynthesis = nil
		return state
	}).WithInvariant("returns nil when input revisions are out of lockstep with different revision numbers", func(state newOpTestState, op *op) bool {
		if !state.hasInputsOutOfLockstep() {
			return true
		}
		return op == nil
	}).WithInvariant("returns nil when composition has fewer input revisions than required", func(state newOpTestState, op *op) bool {
		if !state.hasInsufficientInputs() {
			return true
		}
		return op == nil
	}).WithInvariant("returns nil when composition has deletion timestamp set", func(state newOpTestState, op *op) bool {
		if !state.isCompositionDeleting() {
			return true
		}
		return op == nil
	}).WithInvariant("returns nil when composition has no finalizers", func(state newOpTestState, op *op) bool {
		if !state.isMissingFinalizer() {
			return true
		}
		return op == nil
	}).WithInvariant("creates initial synthesis operation when current and in-flight synthesis are both nil", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || !state.hasNilSynthesis() {
			return true
		}
		return op != nil && op.Reason == initialSynthesisOp && !op.Reason.Deferred()
	}).WithInvariant("creates forced resynthesis operation when composition force resynthesis flag is set", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || !state.comp.ShouldForceResynthesis() {
			return true
		}
		return op != nil && op.Reason == forcedResynthesisOp && !op.Reason.Deferred()
	}).WithInvariant("creates composition modified operation when composition generation differs from observed generation", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || !state.isCompositionModified() {
			return true
		}
		return op != nil && op.Reason == compositionModifiedOp && !op.Reason.Deferred()
	}).WithInvariant("returns nil when composition has ignore side effects flag enabled", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || state.isCompositionModified() || !state.comp.ShouldIgnoreSideEffects() {
			return true
		}
		return op == nil
	}).WithInvariant("creates input modified operation when non-deferred input resource version changed", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || state.isCompositionModified() || state.comp.ShouldIgnoreSideEffects() || !state.hasInputModified() {
			return true
		}
		return op != nil && op.Reason == inputModifiedOp && !op.Reason.Deferred()
	}).WithInvariant("creates deferred input modified operation when deferred input changed and not synthesizing", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || state.isCompositionModified() || state.comp.ShouldIgnoreSideEffects() || state.hasInputModified() || !state.hasDeferredInputModified() {
			return true
		}
		if state.comp.Synthesizing() {
			return op == nil
		}
		return op != nil && op.Reason == deferredInputModifiedOp && op.Reason.Deferred()
	}).WithInvariant("returns nil when synthesizer generation is zero indicating uninitialized state", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || state.isCompositionModified() || state.comp.ShouldIgnoreSideEffects() || state.hasInputModified() || state.hasDeferredInputModified() || state.synth.Generation != 0 {
			return true
		}
		return op == nil
	}).WithInvariant("creates synthesizer modified operation when synthesizer generation increased and not synthesizing", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || state.isCompositionModified() || state.comp.ShouldIgnoreSideEffects() || state.hasInputModified() || state.hasDeferredInputModified() || state.synth.Generation == 0 || !state.isSynthesizerModified() {
			return true
		}
		if state.comp.Synthesizing() {
			return op == nil
		}
		return op != nil && op.Reason == synthesizerModifiedOp && op.Reason.Deferred()
	}).WithInvariant("returns nil when no operation conditions are satisfied in valid state", func(state newOpTestState, op *op) bool {
		if state.hasInvalidState() || state.hasNilSynthesis() || state.comp.ShouldForceResynthesis() || state.isCompositionModified() || state.comp.ShouldIgnoreSideEffects() || state.hasInputModified() || state.hasDeferredInputModified() || state.synth.Generation == 0 || state.isSynthesizerModified() {
			return true
		}
		return op == nil
	}).WithInvariant("operation patch applied to composition creates idempotent state with no further operations", func(state newOpTestState, op *op) bool {
		if op == nil {
			return true
		}

		cli := testutil.NewClient(t)
		comp := state.comp.DeepCopy()
		comp.ResourceVersion = ""
		if err := cli.Create(ctx, comp); err != nil {
			return false
		}
		if err := cli.Status().Update(ctx, comp); err != nil {
			return false
		}

		patchJS, err := json.Marshal(op.BuildPatch())
		if err != nil {
			return false
		}
		if err := cli.Status().Patch(ctx, comp, client.RawPatch(types.JSONPatchType, patchJS)); err != nil {
			return false
		}

		if err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp); err != nil {
			return false
		}

		return newOp(state.synth, comp, time.Time{}) == nil
	}).Evaluate(t)
}

type newOpTestState struct {
	synth          *apiv1.Synthesizer
	comp, original *apiv1.Composition
}

func (s newOpTestState) hasInsufficientInputs() bool { return len(s.comp.Status.InputRevisions) < 2 }
func (s newOpTestState) isCompositionDeleting() bool { return s.comp.DeletionTimestamp != nil }
func (s newOpTestState) isMissingFinalizer() bool    { return len(s.comp.Finalizers) == 0 }

func (s newOpTestState) hasNilSynthesis() bool {
	return s.comp.Status.CurrentSynthesis == nil && s.comp.Status.InFlightSynthesis == nil
}

func (s newOpTestState) hasInvalidState() bool {
	return s.hasInputsOutOfLockstep() || s.hasInsufficientInputs() || s.isCompositionDeleting() || s.isMissingFinalizer()
}

func (s newOpTestState) hasInputsOutOfLockstep() bool {
	return len(s.comp.Status.InputRevisions) >= 2 && s.comp.Status.InputRevisions[0].Revision != nil && s.comp.Status.InputRevisions[1].Revision != nil
}

func (s newOpTestState) isCompositionModified() bool {
	if s.comp.Status.InFlightSynthesis != nil {
		return s.comp.Status.InFlightSynthesis.ObservedCompositionGeneration != s.comp.Generation
	}
	if s.comp.Status.CurrentSynthesis != nil {
		return s.comp.Status.CurrentSynthesis.ObservedCompositionGeneration != s.comp.Generation
	}
	return false
}

func (s newOpTestState) getCurrentSynthesis() *apiv1.Synthesis {
	if s.comp.Status.InFlightSynthesis != nil {
		return s.comp.Status.InFlightSynthesis
	}
	return s.comp.Status.CurrentSynthesis
}

func (s newOpTestState) hasInputModified() bool {
	syn := s.getCurrentSynthesis()
	if syn == nil {
		return false
	}
	return len(s.comp.Status.InputRevisions) >= 1 &&
		len(syn.InputRevisions) >= 1 &&
		s.comp.Status.InputRevisions[0].ResourceVersion != syn.InputRevisions[0].ResourceVersion
}

func (s newOpTestState) hasDeferredInputModified() bool {
	syn := s.getCurrentSynthesis()
	if syn == nil {
		return false
	}
	return len(s.comp.Status.InputRevisions) >= 2 &&
		len(syn.InputRevisions) >= 2 &&
		s.comp.Status.InputRevisions[1].ResourceVersion != syn.InputRevisions[1].ResourceVersion
}

func (s newOpTestState) isSynthesizerModified() bool {
	syn := s.getCurrentSynthesis()
	if syn == nil {
		return false
	}
	return s.synth.Generation != 11 && s.synth.Generation != 0 &&
		syn.ObservedSynthesizerGeneration > 0 && syn.ObservedSynthesizerGeneration < s.synth.Generation
}

func TestFuzzInputChangeCount(t *testing.T) {
	for i := 0; i < 10000; i++ {
		synth := &apiv1.Synthesizer{}
		a := []apiv1.InputRevisions{}
		b := []apiv1.InputRevisions{}

		for i := 0; i < rand.Intn(20); i++ {
			a = append(a, newTestInputRevisions())
		}
		for i := 0; i < rand.Intn(20); i++ {
			b = append(b, newTestInputRevisions())
		}

		for i := 0; i < rand.Intn(20); i++ {
			b = append(b, newTestInputRevisions())
			synth.Spec.Refs = append(synth.Spec.Refs, apiv1.Ref{Key: strconv.Itoa(rand.Intn(10)), Defer: rand.Intn(2) == 0})
		}

		nonDeferred, deferred := inputChangeCount(synth, a, b)

		// No refs means no possible input changes
		if len(synth.Spec.Refs) == 0 {
			assert.Equal(t, 0, nonDeferred)
			assert.Equal(t, 0, deferred)
			continue
		}

		// There isn't much to test for here without re-implementing all of the logic
		// Just make sure it doesn't panic
	}
}

func newTestInputRevisions() apiv1.InputRevisions {
	revs := apiv1.InputRevisions{
		Key:             strconv.Itoa(rand.Intn(20)),
		ResourceVersion: strconv.Itoa(rand.Intn(5)),
	}
	if rand.Intn(3) == 0 {
		revs.Revision = ptr.To(rand.Intn(5))
	}
	return revs
}

func TestOpPriorityBasics(t *testing.T) {
	ops := []*op{
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "deferred-input"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      deferredInputModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "deferred-input-also"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      deferredInputModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "also-not-deferred"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      compositionModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "not-deferred"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      compositionModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "synth-also"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      synthesizerModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "synth"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      synthesizerModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "other-synth"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "other-synth", Generation: 2}},
			Reason:      synthesizerModifiedOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "also-initial"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      initialSynthesisOp,
		},
		{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "initial"}},
			Synthesizer: &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: "synth", Generation: 1}},
			Reason:      initialSynthesisOp,
		},
	}

	for i := 0; i < 100; i++ {
		rand.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
		sort.Slice(ops, func(i, j int) bool { return ops[i].Less(ops[j]) })

		var names []string
		for _, op := range ops {
			names = append(names, string(op.Composition.UID))
		}

		assert.Equal(t, []string{"also-initial", "initial", "also-not-deferred", "not-deferred", "deferred-input", "deferred-input-also", "synth", "other-synth", "synth-also"}, names, "pass: %d", i)
	}
}

func TestOpPrioritySynthRollout(t *testing.T) {
	synth := &apiv1.Synthesizer{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uuid.New().String()), Generation: 1}}

	// Generate a number of compositions that are due to receive a new synthesizer
	ops := []*op{}
	for i := 0; i < 50; i++ {
		ops = append(ops, &op{
			Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uuid.New().String())}},
			Synthesizer: synth,
			Reason:      synthesizerModifiedOp,
		})
	}

	// Each time the synthesizer generation changes, the order of the compositions should change
	var lastOrder []string
	for i := 0; i < 5; i++ {
		synth.Generation++
		for _, op := range ops {
			op.synthRolloutHash = nil // hack to re-calcuate the hash now that the synth generation has changed
		}

		rand.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
		sort.Slice(ops, func(i, j int) bool { return ops[i].Less(ops[j]) })

		var names []string
		for _, op := range ops {
			names = append(names, string(op.Composition.UID))
		}

		assert.NotEqual(t, lastOrder, names)
		lastOrder = names
	}
}

func TestOpPriorityTies(t *testing.T) {
	var synths []*apiv1.Synthesizer
	for i := 0; i < 10; i++ {
		synth := &apiv1.Synthesizer{}
		synth.UID = types.UID(uuid.New().String())
		synth.Generation = int64(rand.Intn(10))
		synths = append(synths, synth)
	}

	ops := []*op{}
	for i := 0; i < 1000; i++ {
		o := &op{Composition: &apiv1.Composition{}}
		o.Composition.UID = types.UID(uuid.New().String())
		o.Synthesizer = synths[rand.Intn(len(synths))]
		o.Reason = allReasons[rand.Intn(len(allReasons))]
		ops = append(ops, o)
	}

	var firstOrder []string
	for i := 0; i < 1000; i++ {
		rand.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
		sort.Slice(ops, func(i, j int) bool { return ops[i].Less(ops[j]) })

		var names []string
		for _, op := range ops {
			names = append(names, string(op.Composition.UID))
		}

		if firstOrder == nil {
			firstOrder = names
		} else if !slices.Equal(firstOrder, names) {
			t.Error("order changed! (omitting specifics to avoid huge logs)")
		}
	}
}

func TestOpPriorityRetries(t *testing.T) {
	ops := []*op{
		{Reason: synthesizerModifiedOp, Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "d"}}},
		{Reason: retrySynthesisOp, NotBefore: time.Unix(2000, 0), Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "a"}}},
		{Reason: retrySynthesisOp, NotBefore: time.Unix(1000, 0), Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "b"}}},
		{Reason: initialSynthesisOp, Composition: &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{UID: "c"}}},
	}

	for i := 0; i < 100; i++ {
		rand.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
		sort.Slice(ops, func(i, j int) bool { return ops[i].Less(ops[j]) })

		var names []string
		for _, op := range ops {
			names = append(names, string(op.Composition.UID))
		}

		assert.Equal(t, []string{"c", "d", "a", "b"}, names)
	}
}

// TestOpNewerInputInSynthesis covers an edge case where the synthesizer sees a newer version of an input
// than the watch controller. This might happen during synthesis if the input is changing frequently.
// In extreme cases synthesis might be blocked since it will be re-dispatched for every input change.
func TestOpNewerInputInSynthesis(t *testing.T) {
	synth := &apiv1.Synthesizer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-synth", Generation: 11},
		Spec: apiv1.SynthesizerSpec{
			Refs: []apiv1.Ref{
				{Key: "foo"},
				{Key: "bar", Defer: true},
			},
		},
	}

	initTS := time.Date(8000, 0, 0, 0, 0, 0, 0, time.UTC)
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "test-comp", Finalizers: []string{"eno.azure.io/cleanup"}, Generation: 1},
		Spec: apiv1.CompositionSpec{
			Bindings: []apiv1.Binding{
				{Key: "foo", Resource: apiv1.ResourceBinding{Name: "foo"}},
			},
		},
		Status: apiv1.CompositionStatus{
			InFlightSynthesis: &apiv1.Synthesis{
				ObservedCompositionGeneration: 1,
				ObservedSynthesizerGeneration: 11,
				Synthesized:                   ptr.To(metav1.Now()),
				Initialized:                   ptr.To(metav1.NewTime(initTS)),
				UUID:                          "initial-uuid",
				InputRevisions: []apiv1.InputRevisions{
					{Key: "foo", ResourceVersion: "2"},
				},
			},
			InputRevisions: []apiv1.InputRevisions{
				{Key: "foo", ResourceVersion: "1"},
			},
		},
	}

	assert.Nil(t, newOp(synth, comp, time.Time{}))
}
