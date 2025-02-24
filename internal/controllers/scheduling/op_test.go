package scheduling

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strconv"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestFuzzNewOp(t *testing.T) {
	ctx := testutil.NewContext(t)

	// Generate all possible test cases
	var testCases [][13]bool
	for i := 0; i < 1<<13; i++ {
		var args [13]bool
		for j := 0; j < 13; j++ {
			args[j] = (i>>j)&1 == 1
		}
		testCases = append(testCases, args)
	}

	for _, args := range testCases {
		var (
			inputModified         = args[0]
			deferredInputModified = args[1]
			inputsMissing         = args[2]
			inputsOutOfLockstep   = args[3]
			ignoreSideEffects     = args[4]
			missingFinalizer      = args[5]
			synthModified         = args[6]
			synthGenZero          = args[7]
			forceResynth          = args[8]
			synthesizing          = args[9]
			compDeleting          = args[10]
			compModified          = args[11]
			nilSynthesis          = args[12]
		)

		// We purposefully do not log every set of args because doing so would generate copious amounts of log output
		args := fmt.Sprintf("inputModified=%t,deferredInputModified=%t,inputsMissing=%t,inputsOutOfLockstep=%t,ignoreSideEffects=%t,missingFinalizer=%t,synthModified=%t,synthGenZero=%t,forceResynth=%t,synthesizing=%t,compDeleting=%t,compModified=%t,nilSynthesis=%t", inputModified, deferredInputModified, inputsMissing, inputsOutOfLockstep, ignoreSideEffects, missingFinalizer, synthModified, synthGenZero, forceResynth, synthesizing, compDeleting, compModified, nilSynthesis)

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
		original := comp.DeepCopy()

		// Mutate the composition/synthesizer based on the test case
		if inputModified {
			comp.Status.InputRevisions[0].ResourceVersion = "modified"
		}
		if deferredInputModified {
			comp.Status.InputRevisions[1].ResourceVersion = "modified"
		}
		if inputsOutOfLockstep {
			comp.Status.InputRevisions[0].Revision = ptr.To(123)
			comp.Status.InputRevisions[1].Revision = ptr.To(234)
		}
		if inputsMissing {
			comp.Status.InputRevisions = comp.Status.InputRevisions[:1]
		}
		if ignoreSideEffects {
			comp.EnableIgnoreSideEffects()
		}
		if missingFinalizer {
			comp.Finalizers = nil
		}
		if synthModified {
			synth.Generation = 234
		}
		if synthGenZero {
			synth.Generation = 0
		}
		if forceResynth {
			comp.ForceResynthesis()
		}
		if synthesizing {
			comp.Status.CurrentSynthesis.Synthesized = nil
		}
		if compDeleting {
			comp.DeletionTimestamp = ptr.To(metav1.Now())
		}
		if compModified {
			comp.Status.CurrentSynthesis.ObservedCompositionGeneration = 123
		}
		if nilSynthesis {
			comp.Status.CurrentSynthesis = nil
		}

		op := newOp(synth, comp)

		// Prove out the invariants
		switch {
		case inputsOutOfLockstep || inputsMissing || compDeleting || missingFinalizer:
			assert.Nil(t, op)

		case nilSynthesis:
			require.NotNil(t, op, args)
			assert.Equal(t, initialSynthesisOp, op.Reason, args)
			assert.False(t, op.Reason.Deferred(), args)

		case forceResynth:
			require.NotNil(t, op, args)
			assert.Equal(t, forcedResynthesisOp, op.Reason, args)
			assert.False(t, op.Reason.Deferred(), args)

		case compModified:
			require.NotNil(t, op, args)
			assert.Equal(t, compositionModifiedOp, op.Reason, args)
			assert.False(t, op.Reason.Deferred(), args)

		case ignoreSideEffects:
			require.Nil(t, op, args)

		case inputModified:
			require.NotNil(t, op, args)
			assert.Equal(t, inputModifiedOp, op.Reason, args)
			assert.False(t, op.Reason.Deferred(), args)

		case deferredInputModified:
			if synthesizing {
				require.Nil(t, op, args)
			} else {
				require.NotNil(t, op, args)
				assert.Equal(t, deferredInputModifiedOp, op.Reason, args)
				assert.True(t, op.Reason.Deferred(), args)
			}

		case synthGenZero:
			require.Nil(t, op, args)

		case synthModified:
			if synthesizing {
				require.Nil(t, op, args)
			} else {
				require.NotNil(t, op, args)
				assert.Equal(t, synthesizerModifiedOp, op.Reason, args)
				assert.True(t, op.Reason.Deferred(), args)
			}
		}

		if op == nil {
			continue
		}

		// newOp always returns nil when given the same composition immediately after the op patch has been applied
		// (proves synthesis cannot get stuck in a positive feedback loop)
		{
			cli := testutil.NewClient(t)
			comp.ResourceVersion = ""
			require.NoError(t, cli.Create(ctx, comp))
			require.NoError(t, cli.Status().Update(ctx, comp))

			patchJS, err := json.Marshal(op.BuildPatch())
			require.NoError(t, err)
			err = cli.Status().Patch(ctx, comp, client.RawPatch(types.JSONPatchType, patchJS))
			require.NoError(t, err)

			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			assert.Nil(t, newOp(synth, comp), args)
		}

		// Patches against the original, non-mutated test composition should always fail.
		// This proves that the patch contains a `test` op for each field considered by newOp.
		{
			cli := testutil.NewClient(t)
			require.NoError(t, cli.Create(ctx, original))
			require.NoError(t, cli.Status().Update(ctx, original))

			patchJS, err := json.Marshal(op.BuildPatch())
			require.NoError(t, err)
			err = cli.Status().Patch(ctx, original, client.RawPatch(types.JSONPatchType, patchJS))
			assert.Error(t, err, args)
		}
	}
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
			CurrentSynthesis: &apiv1.Synthesis{
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

	assert.Nil(t, newOp(synth, comp))
}
