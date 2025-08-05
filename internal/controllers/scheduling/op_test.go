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

type newOpTestState struct {
	synth          *apiv1.Synthesizer
	comp, original *apiv1.Composition
}

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
	}).WithInvariant("follows switch logic precedence", func(state newOpTestState, op *op) bool {
		// Check invalid states first (these return nil)
		inputsOutOfLockstep := len(state.comp.Status.InputRevisions) >= 2 &&
			state.comp.Status.InputRevisions[0].Revision != nil &&
			state.comp.Status.InputRevisions[1].Revision != nil
		inputsMissing := len(state.comp.Status.InputRevisions) < 2
		compDeleting := state.comp.DeletionTimestamp != nil
		missingFinalizer := len(state.comp.Finalizers) == 0

		if inputsOutOfLockstep || inputsMissing || compDeleting || missingFinalizer {
			return op == nil
		}

		// Check nilSynthesis (highest priority for non-nil ops)
		nilSynthesis := state.comp.Status.CurrentSynthesis == nil && state.comp.Status.InFlightSynthesis == nil
		if nilSynthesis {
			return op != nil && op.Reason == initialSynthesisOp && !op.Reason.Deferred()
		}

		// Check forceResynth
		if state.comp.ShouldForceResynthesis() {
			return op != nil && op.Reason == forcedResynthesisOp && !op.Reason.Deferred()
		}

		// Check compModified - this includes InFlightSynthesis case
		if state.comp.Status.InFlightSynthesis != nil {
			compModified := state.comp.Status.InFlightSynthesis.ObservedCompositionGeneration != state.comp.Generation
			if compModified {
				return op != nil && op.Reason == compositionModifiedOp && !op.Reason.Deferred()
			}
		} else if state.comp.Status.CurrentSynthesis != nil {
			compModified := state.comp.Status.CurrentSynthesis.ObservedCompositionGeneration != state.comp.Generation
			if compModified {
				return op != nil && op.Reason == compositionModifiedOp && !op.Reason.Deferred()
			}
		}

		// Check ignoreSideEffects (returns nil)
		if state.comp.ShouldIgnoreSideEffects() {
			return op == nil
		}

		// Now we check input changes - baseline is CurrentSynthesis OR InFlightSynthesis
		var syn *apiv1.Synthesis
		if state.comp.Status.InFlightSynthesis != nil {
			syn = state.comp.Status.InFlightSynthesis
		} else {
			syn = state.comp.Status.CurrentSynthesis
		}

		if syn == nil {
			// This should not happen as we already checked nilSynthesis
			return op == nil
		}

		// Check inputModified - compare current input revisions with synthesis baseline
		inputModified := len(state.comp.Status.InputRevisions) >= 1 &&
			len(syn.InputRevisions) >= 1 &&
			state.comp.Status.InputRevisions[0].ResourceVersion != syn.InputRevisions[0].ResourceVersion
		if inputModified {
			return op != nil && op.Reason == inputModifiedOp && !op.Reason.Deferred()
		}

		// Check deferredInputModified
		deferredInputModified := len(state.comp.Status.InputRevisions) >= 2 &&
			len(syn.InputRevisions) >= 2 &&
			state.comp.Status.InputRevisions[1].ResourceVersion != syn.InputRevisions[1].ResourceVersion

		if deferredInputModified {
			// Deferred ops won't replace an in-flight synthesis
			if state.comp.Synthesizing() {
				return op == nil
			} else {
				return op != nil && op.Reason == deferredInputModifiedOp && op.Reason.Deferred()
			}
		}

		// Check synthGenZero (returns nil)
		if state.synth.Generation == 0 {
			return op == nil
		}

		// Check synthModified
		synthModified := state.synth.Generation != 11 && state.synth.Generation != 0 &&
			syn.ObservedSynthesizerGeneration > 0 && syn.ObservedSynthesizerGeneration < state.synth.Generation
		if synthModified {
			// Deferred ops won't replace an in-flight synthesis
			if state.comp.Synthesizing() {
				return op == nil
			} else {
				return op != nil && op.Reason == synthesizerModifiedOp && op.Reason.Deferred()
			}
		}

		// If none of the above conditions are met, should return nil
		return op == nil
	}).WithInvariant("op patch creates idempotent state", func(state newOpTestState, op *op) bool {
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
