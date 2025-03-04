package scheduling

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"reflect"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type op struct {
	Synthesizer *apiv1.Synthesizer
	Composition *apiv1.Composition
	Reason      opReason

	Dispatched time.Time

	id               uuid.UUID // set when patch is built
	synthRolloutHash []byte    // memoized
}

func newOp(synth *apiv1.Synthesizer, comp *apiv1.Composition) *op {
	o := &op{Synthesizer: synth, Composition: comp}

	var ok bool
	o.Reason, ok = classifyOp(synth, comp)
	if !ok {
		return nil
	}

	// Deferred ops have a special property: they won't replace an in-flight synthesis
	// This protects frequent synth/input changes from effectively blocking synthesis
	if o.Reason.Deferred() && comp.Synthesizing() {
		return nil
	}

	return o
}

func classifyOp(synth *apiv1.Synthesizer, comp *apiv1.Composition) (opReason, bool) {
	switch {
	case comp.DeletionTimestamp != nil || !comp.InputsExist(synth) || comp.InputsOutOfLockstep(synth) || !controllerutil.ContainsFinalizer(comp, "eno.azure.io/cleanup"):
		return 0, false

	case (comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil) && comp.Status.PendingSynthesis == nil:
		return initialSynthesisOp, true

	case comp.ShouldForceResynthesis():
		return forcedResynthesisOp, true

	case compositionHasBeenModified(comp):
		return compositionModifiedOp, true

	case comp.ShouldIgnoreSideEffects():
		return 0, false
	}

	syn := comp.Status.CurrentSynthesis
	if comp.Status.PendingSynthesis != nil {
		syn = comp.Status.PendingSynthesis
	}

	nonDeferredInputChanges, deferredInputChanges := inputChangeCount(synth, comp.Status.InputRevisions, syn.InputRevisions)
	if nonDeferredInputChanges > 0 {
		return inputModifiedOp, true
	}

	if deferredInputChanges > 0 {
		return deferredInputModifiedOp, true
	}

	if syn.ObservedSynthesizerGeneration > 0 && syn.ObservedSynthesizerGeneration < synth.Generation {
		return synthesizerModifiedOp, true
	}

	return 0, false
}

func compositionHasBeenModified(comp *apiv1.Composition) bool {
	if comp.Status.PendingSynthesis != nil {
		return comp.Status.PendingSynthesis.ObservedCompositionGeneration != comp.Generation
	}
	return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation
}

func (o *op) Less(than *op) bool {
	if o.Reason == synthesizerModifiedOp && than.Reason == synthesizerModifiedOp {
		cmp := bytes.Compare(o.SynthRolloutOrderHash(), than.SynthRolloutOrderHash())
		if cmp != 0 {
			return cmp > 0
		}
	}

	if o.Reason == than.Reason {
		return o.Composition.UID < than.Composition.UID
	}

	return o.Reason < than.Reason
}

// SynthRolloutOrderHash returns a hash that represents this composition's order in the rollout of a particular synthesizer generation.
// This mechanism maintains determinism while shuffling the rollout order of every synthesizer change.
func (o *op) SynthRolloutOrderHash() []byte {
	if o.synthRolloutHash == nil {
		hash := fnv.New64()
		fmt.Fprintf(hash, "%s:%d:%s", o.Synthesizer.UID, o.Synthesizer.Generation, o.Composition.UID)
		o.synthRolloutHash = hash.Sum(nil)
	}
	return o.synthRolloutHash
}

func (o *op) HasBeenPatched(ctx context.Context, cli client.Reader, grace time.Duration) (bool, time.Duration, error) {
	logger := logr.FromContextOrDiscard(ctx)

	wait := time.Since(o.Dispatched)
	if wait > grace {
		logger.V(1).Info("operation cache grace period expired", "synthesisUUID", o.id)
		return true, wait, nil
	}

	comp := &apiv1.Composition{}
	err := cli.Get(ctx, client.ObjectKeyFromObject(o.Composition), comp)
	if err != nil {
		return false, 0, err
	}

	if syn := comp.Status.CurrentSynthesis; syn != nil && syn.UUID == o.id.String() {
		return true, wait, nil
	}
	if syn := comp.Status.PendingSynthesis; syn != nil && syn.UUID == o.id.String() {
		return true, wait, nil
	}
	return false, wait, nil
}

func (o *op) BuildPatch() any {
	ops := []jsonPatch{}

	if o.id == uuid.Nil {
		// defer generating the uuid until we know the op is definitely going to be dispatched
		o.id = uuid.Must(uuid.NewRandom())
	}

	// Initialize the status if it's nil (zero value struct on the client == nil on the server side)
	if reflect.DeepEqual(o.Composition.Status, apiv1.CompositionStatus{}) {
		ops = append(ops,
			jsonPatch{Op: "test", Path: "/status", Value: nil},
			jsonPatch{Op: "add", Path: "/status", Value: map[string]any{}})
	}

	// The input watch controller might have concurrently modified the input revisions
	ops = append(ops, jsonPatch{Op: "test", Path: "/status/inputRevisions", Value: o.Composition.Status.InputRevisions})

	// Protect against zombie leaders running this controller
	if syn := o.Composition.Status.PendingSynthesis; syn == nil {
		ops = append(ops, jsonPatch{Op: "test", Path: "/status/pendingSynthesis", Value: nil})
	} else {
		ops = append(ops,
			jsonPatch{Op: "test", Path: "/status/pendingSynthesis/uuid", Value: syn.UUID},
			jsonPatch{Op: "test", Path: "/status/pendingSynthesis/observedCompositionGeneration", Value: syn.ObservedCompositionGeneration},
			jsonPatch{Op: "test", Path: "/status/pendingSynthesis/synthesized", Value: syn.Synthesized})
	}

	ops = append(ops, jsonPatch{
		Op:   "replace",
		Path: "/status/pendingSynthesis",
		Value: map[string]any{
			"observedCompositionGeneration": o.Composition.Generation,
			"initialized":                   time.Now().Format(time.RFC3339),
			"uuid":                          o.id.String(),
			"deferred":                      o.Reason.Deferred(),
		},
	})

	return ops
}

type jsonPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

type opReason int

const (
	initialSynthesisOp opReason = iota
	forcedResynthesisOp
	compositionModifiedOp
	inputModifiedOp
	deferredInputModifiedOp
	synthesizerModifiedOp
)

var allReasons = []opReason{initialSynthesisOp, forcedResynthesisOp, compositionModifiedOp, inputModifiedOp, deferredInputModifiedOp, synthesizerModifiedOp}

func (r opReason) Deferred() bool { return r == deferredInputModifiedOp || r == synthesizerModifiedOp }

func (r opReason) String() string {
	switch r {
	case initialSynthesisOp:
		return "InitialSynthesis"
	case forcedResynthesisOp:
		return "ForcedResynthesis"
	case compositionModifiedOp:
		return "CompositionModified"
	case inputModifiedOp:
		return "InputModified"
	case deferredInputModifiedOp:
		return "DeferredInputModified"
	case synthesizerModifiedOp:
		return "SynthesizerModified"
	default:
		return "Unknown"
	}
}

func inputChangeCount(synth *apiv1.Synthesizer, a, b []apiv1.InputRevisions) (nonDeferred, deferred int) {
	refsByKey := map[string]apiv1.Ref{}
	for _, ref := range synth.Spec.Refs {
		ref := ref
		refsByKey[ref.Key] = ref
	}

	bByKey := map[string]apiv1.InputRevisions{}
	for _, br := range b {
		bByKey[br.Key] = br
	}

	for _, ar := range a {
		ref, exists := refsByKey[ar.Key]
		if !exists {
			continue
		}
		br, exists := bByKey[ar.Key]
		if !exists {
			continue
		}

		if br.Less(ar) {
			if ref.Defer {
				deferred++
			} else {
				nonDeferred++
			}
		}
	}

	return nonDeferred, deferred
}
