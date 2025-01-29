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
	syn := comp.Status.CurrentSynthesis
	o := &op{Synthesizer: synth, Composition: comp}

	switch {
	case comp.DeletionTimestamp != nil || !comp.InputsExist(synth) || comp.InputsOutOfLockstep(synth) || !controllerutil.ContainsFinalizer(comp, "eno.azure.io/cleanup"):
		return nil

	case syn == nil:
		o.Reason = initialSynthesisOp
		return o

	case comp.ShouldForceResynthesis():
		o.Reason = forcedResynthesisOp
		return o

	case comp.Synthesizing():
		return nil

	case syn.ObservedCompositionGeneration != comp.Generation:
		o.Reason = compositionModifiedOp
		return o

	case comp.ShouldIgnoreSideEffects():
		return nil
	}

	nonDeferredInputChanges, deferredInputChanges := inputChangeCount(synth, comp.Status.InputRevisions, syn.InputRevisions)
	if nonDeferredInputChanges > 0 && syn.Synthesized != nil {
		o.Reason = inputModifiedOp
		return o
	}
	if deferredInputChanges > 0 && syn.Synthesized != nil {
		o.Reason = deferredInputModifiedOp
		return o
	}

	if syn.ObservedSynthesizerGeneration > 0 && syn.ObservedSynthesizerGeneration < synth.Generation {
		o.Reason = synthesizerModifiedOp
		return o
	}

	return nil
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

	return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID == o.id.String(), wait, nil
}

func (o *op) BuildPatch() any {
	ops := []jsonPatch{}

	if o.id == uuid.Nil {
		// defer generating the uuid until we know the op is definitely going to be dispatched
		o.id = uuid.Must(uuid.NewRandom())
	}

	if reflect.DeepEqual(o.Composition.Status, apiv1.CompositionStatus{}) {
		ops = append(ops,
			jsonPatch{Op: "test", Path: "/status", Value: nil},
			jsonPatch{Op: "add", Path: "/status", Value: map[string]any{}})
	}

	ops = append(ops, jsonPatch{Op: "test", Path: "/status/inputRevisions", Value: o.Composition.Status.InputRevisions})

	if syn := o.Composition.Status.CurrentSynthesis; syn == nil {
		ops = append(ops, jsonPatch{Op: "test", Path: "/status/currentSynthesis", Value: nil})
	} else {
		ops = append(ops,
			jsonPatch{Op: "test", Path: "/status/currentSynthesis/uuid", Value: syn.UUID},
			jsonPatch{Op: "test", Path: "/status/currentSynthesis/observedCompositionGeneration", Value: syn.ObservedCompositionGeneration},
			jsonPatch{Op: "test", Path: "/status/currentSynthesis/synthesized", Value: syn.Synthesized})

		if syn.Synthesized != nil && !syn.Failed() {
			ops = append(ops, jsonPatch{Op: "replace", Path: "/status/previousSynthesis", Value: syn})
		}
	}

	ops = append(ops, jsonPatch{
		Op:   "replace",
		Path: "/status/currentSynthesis",
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

		if !ar.Equal(br) {
			if ref.Defer {
				deferred++
			} else {
				nonDeferred++
			}
		}
	}

	return nonDeferred, deferred
}
