package inputs

import (
	"slices"

	apiv1 "github.com/Azure/eno/api/v1"
)

// Exist returns true when all of the inputs required by a synthesizer are represented by the given composition's status.
// Optional refs are not required to exist and will not cause this function to return false.
func Exist(syn *apiv1.Synthesizer, c *apiv1.Composition) bool {
	for _, ref := range syn.Spec.Refs {
		// Skip optional refs - they are not required to exist
		if ref.Optional {
			continue
		}

		found := slices.ContainsFunc(c.Status.InputRevisions, func(current apiv1.InputRevisions) bool {
			return ref.Key == current.Key
		})
		if !found {
			return false
		}
	}
	return true
}

// OutOfLockstep returns true when one or more inputs that specify a revision do not match the others.
// It also returns true if any revision is derived from a synthesizer/composition generation older than the ones provided.
func OutOfLockstep(synth *apiv1.Synthesizer, comp *apiv1.Composition, revs []apiv1.InputRevisions) bool {
	// First, the the max revision across all bindings
	var maxRevision *int
	for _, rev := range revs {
		if rev.SynthesizerGeneration != nil && *rev.SynthesizerGeneration < synth.Generation {
			return true
		}
		if rev.CompositionGeneration != nil && *rev.CompositionGeneration < comp.Generation {
			return true
		}
		if rev.Revision == nil {
			continue
		}
		if maxRevision == nil {
			maxRevision = rev.Revision
			continue
		}
		if *rev.Revision > *maxRevision {
			maxRevision = rev.Revision
		}
	}
	if maxRevision == nil {
		return false // no inputs declare a revision, so we should assume they're in sync
	}

	// Now given the max, make sure all inputs with a revision match it
	for _, rev := range revs {
		if rev.Revision != nil && *maxRevision != *rev.Revision {
			return true
		}
	}
	return false
}
