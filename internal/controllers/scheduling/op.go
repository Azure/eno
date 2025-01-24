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
	Composition   *apiv1.Composition
	DeferredUntil *time.Time
	Reason        string
}

// TODO: passing both cooldown and lastDeferred is a bit awkward
func newOp(synth *apiv1.Synthesizer, comp *apiv1.Composition, cooldown time.Duration, lastDeferred time.Time) *op {
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
		until := lastDeferred.Add(cooldown)
		o.DeferredUntil = &until
		o.Reason = "DeferredInputModified"
		return o
	}

	if syn.ObservedSynthesizerGeneration > 0 && syn.ObservedSynthesizerGeneration < synth.Generation && !comp.ShouldIgnoreSideEffects() {
		until := lastDeferred.Add(cooldown)
		o.DeferredUntil = &until
		o.Reason = "SynthesizerModified"
		return o
	}

	return nil
}

func (o *op) LowerPriority(other *op) bool {
	// TODO: Remember to shuffle items within the same priority to distribute deferred rollouts
	return false
}

func (o *op) Deferred() bool {
	return o.DeferredUntil != nil && o.DeferredUntil.After(time.Now())
}

func (o *op) String() string {
	deferredFor := 0
	if o.DeferredUntil != nil {
		deferredFor = int(time.Until(*o.DeferredUntil).Abs().Milliseconds())
	}
	return fmt.Sprintf("op{composition=%s/%s, reason=%s, deferredFor=%dms}", o.Composition.Namespace, o.Composition.Name, o.Reason, deferredFor)
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

	ops = append(ops, map[string]any{
		"op":   "replace",
		"path": "/status/currentSynthesis",
		"value": map[string]any{
			"observedCompositionGeneration": o.Composition.Generation,
			"initialized":                   time.Now().Format(time.RFC3339),
			"uuid":                          uuid.NewString(),
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
