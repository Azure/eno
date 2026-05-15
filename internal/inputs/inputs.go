package inputs

import (
	"slices"

	apiv1 "github.com/Azure/eno/api/v1"
)

// Exist returns true when all of the inputs required by a synthesizer are represented by the given composition's status.
// Optional refs are not required to exist and will not cause this function to return false.
func Exist(syn *apiv1.Synthesizer, c *apiv1.Composition) bool {
	return len(Missing(syn, c)) == 0
}

// Missing returns the keys of any non-optional inputs required by the synthesizer that
// are not represented in the given composition's status.
func Missing(syn *apiv1.Synthesizer, c *apiv1.Composition) []string {
	var missing []string
	for _, ref := range syn.Spec.Refs {
		// Skip optional refs - they are not required to exist
		if ref.Optional {
			continue
		}

		found := slices.ContainsFunc(c.Status.InputRevisions, func(current apiv1.InputRevisions) bool {
			return ref.Key == current.Key
		})
		if !found {
			missing = append(missing, ref.Key)
		}
	}
	return missing
}

// OutOfLockstep returns true when one or more inputs that specify a revision do not match the others.
// It also returns true if any revision is derived from a synthesizer/composition generation older than the ones provided.
func OutOfLockstep(synth *apiv1.Synthesizer, comp *apiv1.Composition, revs []apiv1.InputRevisions) bool {
	return len(Mismatched(synth, comp, revs)) > 0
}

// MismatchedInput describes a single input revision that is out of lockstep with the others
// (or with the current synthesizer/composition generation). It is intended for logging/telemetry.
type MismatchedInput struct {
	Key                   string
	Revision              *int
	MaxRevision           *int
	SynthesizerGeneration *int64
	CompositionGeneration *int64
}

// Mismatched returns the set of input revisions that are out of lockstep with the others, or
// derived from a synthesizer/composition generation older than the current ones.
func Mismatched(synth *apiv1.Synthesizer, comp *apiv1.Composition, revs []apiv1.InputRevisions) []MismatchedInput {
	var mismatched []MismatchedInput

	// First, find the max revision across all bindings
	var maxRevision *int
	for _, rev := range revs {
		if rev.Revision == nil {
			continue
		}
		if maxRevision == nil || *rev.Revision > *maxRevision {
			maxRevision = rev.Revision
		}
	}

	for _, rev := range revs {
		stale := false
		if rev.SynthesizerGeneration != nil && *rev.SynthesizerGeneration < synth.Generation {
			stale = true
		}
		if rev.CompositionGeneration != nil && *rev.CompositionGeneration < comp.Generation {
			stale = true
		}
		revMismatch := maxRevision != nil && rev.Revision != nil && *rev.Revision != *maxRevision
		if !stale && !revMismatch {
			continue
		}
		mismatched = append(mismatched, MismatchedInput{
			Key:                   rev.Key,
			Revision:              rev.Revision,
			MaxRevision:           maxRevision,
			SynthesizerGeneration: rev.SynthesizerGeneration,
			CompositionGeneration: rev.CompositionGeneration,
		})
	}
	return mismatched
}
