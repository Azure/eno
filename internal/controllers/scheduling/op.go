package scheduling

import (
	"fmt"
	"sort"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/uuid"
)

func prioritizeOps(queue []*op) {
	sort.Slice(queue, func(i, j int) bool { return queue[i].LowerPriority(queue[j]) })
}

type op struct {
	Composition *apiv1.Composition
	OnlyAfter   *time.Time
	Reason      string
}

func newOp(synth *apiv1.Synthesizer, comp *apiv1.Composition, nextSafeDeferral time.Time) *op {
	if (!comp.InputsExist(synth) || comp.InputsOutOfLockstep(synth)) && comp.DeletionTimestamp == nil {
		return nil // wait for inputs
	}

	// TODO: Skip if deleting?

	syn := comp.Status.CurrentSynthesis
	o := &op{Composition: comp}
	if syn == nil {
		o.Reason = "InitialSynthesis"
		return o
	}

	if uuid := comp.GetAnnotations()["eno.azure.io/force-resynthesis"]; uuid != "" && uuid == syn.UUID {
		o.Reason = "ForcedResynthesis"
		return o
	}

	if syn.ObservedCompositionGeneration != comp.Generation {
		o.Reason = "CompositionModified"
		return o
	}

	eq, deferredEq := inputRevisionsEqual(synth, comp.Status.InputRevisions, syn.InputRevisions)
	if !eq && syn.Synthesized != nil && !comp.ShouldIgnoreSideEffects() {
		o.Reason = "InputModified"
		return o
	}
	if !deferredEq && syn.Synthesized != nil && !comp.ShouldIgnoreSideEffects() {
		o.Reason = "DeferredInputModified"
		o.OnlyAfter = &nextSafeDeferral
		return o
	}

	if syn.ObservedSynthesizerGeneration > 0 && syn.ObservedSynthesizerGeneration < synth.Generation && !comp.ShouldIgnoreSideEffects() {
		o.Reason = "SynthesizerModified"
		o.OnlyAfter = &nextSafeDeferral
		return o
	}

	return nil
}

func (o *op) LowerPriority(other *op) bool {
	// TODO: Remember to shuffle items within the same priority
	return false
}

func (o *op) Deferred() bool {
	return o.OnlyAfter != nil && o.OnlyAfter.After(time.Now())
}

func (o *op) String() string {
	deferredFor := 0
	if o.OnlyAfter != nil {
		deferredFor = max(0, int(time.Until(*o.OnlyAfter).Milliseconds()))
	}
	return fmt.Sprintf("op{composition=%s/%s, reason=%s, deferred=%t, wait=%dms}", o.Composition.Namespace, o.Composition.Name, o.Reason, o.Deferred(), deferredFor)
}

func (o *op) Patch() any {
	ops := []map[string]any{}

	if o.Composition.Status.Zero() {
		ops = append(ops,
			map[string]any{
				"op":    "test",
				"path":  "/status",
				"value": nil,
			},
			map[string]any{
				"op":    "add",
				"path":  "/status",
				"value": map[string]any{},
			})
	}

	if syn := o.Composition.Status.CurrentSynthesis; syn != nil {
		ops = append(ops, map[string]any{
			"op":    "test",
			"path":  "/status/currentSynthesis/uuid",
			"value": syn.UUID,
		})

		if syn.Synthesized != nil && !syn.Failed() {
			ops = append(ops, map[string]any{
				"op":    "replace",
				"path":  "/status/previousSynthesis",
				"value": syn,
			})
		}
	}

	deferred := o.OnlyAfter != nil
	ops = append(ops, map[string]any{
		"op":   "replace",
		"path": "/status/currentSynthesis",
		"value": map[string]any{
			"observedCompositionGeneration": o.Composition.Generation,
			"initialized":                   time.Now().Format(time.RFC3339),
			"uuid":                          uuid.NewString(),
			"deferred":                      deferred,
		},
	})

	return ops
}

// inputRevisionsEqual compares two sets of input revisions and returns two bools:
// - equal: true when all non-deferred input revisions are equal
// - deferred: true when all deferred inputs are equal
func inputRevisionsEqual(synth *apiv1.Synthesizer, a, b []apiv1.InputRevisions) (bool /*equal*/, bool /*deferred*/) {
	if len(a) != len(b) {
		return false, false
	}

	refsByKey := map[string]apiv1.Ref{}
	for _, ref := range synth.Spec.Refs {
		ref := ref
		refsByKey[ref.Key] = ref
	}

	sort.Slice(a, func(i, j int) bool { return a[i].Key < a[j].Key })
	sort.Slice(b, func(i, j int) bool { return b[i].Key < b[j].Key })

	var equal int
	var deferred int
	for i, ar := range a {
		br := b[i]
		if ref, exists := refsByKey[ar.Key]; exists && ref.Defer {
			if !ar.Equal(br) {
				deferred++
			}

			equal++
			continue
		}

		if ar.Equal(br) {
			equal++
		}
	}

	return equal == len(a), deferred == 0
}
