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

// Expected returns the keys of all non-optional inputs declared by the synthesizer.
func Expected(syn *apiv1.Synthesizer) []string {
	var expected []string
	for _, ref := range syn.Spec.Refs {
		if ref.Optional {
			continue
		}
		expected = append(expected, ref.Key)
	}
	return expected
}

// OutOfLockstep returns true when one or more inputs that specify a revision do not match the others.
// It also returns true if any revision is derived from a synthesizer/composition generation older than the ones provided.
func OutOfLockstep(synth *apiv1.Synthesizer, comp *apiv1.Composition, revs []apiv1.InputRevisions) bool {
	return len(Mismatched(synth, comp, revs)) > 0
}

// MismatchedInput describes a single input revision that is out of lockstep,
// either with peer revisions or with the current synthesizer/composition
// generation. Intended for logging only. Numeric fields are 0 when unknown.
type MismatchedInput struct {
	Key                   string
	Revision              int   // 0 if unset
	MaxRevision           int   // 0 if no peer revision was observed
	SynthesizerGeneration int64 // 0 if unset
	CompositionGeneration int64 // 0 if unset
}

// Mismatched returns the set of input revisions that are out of lockstep with the others, or
// derived from a synthesizer/composition generation older than the current ones.
func Mismatched(synth *apiv1.Synthesizer, comp *apiv1.Composition, revs []apiv1.InputRevisions) []MismatchedInput {
	var mismatched []MismatchedInput

	// First, find the max revision across all bindings
	maxRevision := 0
	maxSet := false
	for _, rev := range revs {
		if rev.Revision == nil {
			continue
		}
		if !maxSet || *rev.Revision > maxRevision {
			maxRevision = *rev.Revision
			maxSet = true
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
		revMismatch := maxSet && rev.Revision != nil && *rev.Revision != maxRevision
		if !stale && !revMismatch {
			continue
		}
		entry := MismatchedInput{Key: rev.Key}
		if maxSet {
			entry.MaxRevision = maxRevision
		}
		if rev.Revision != nil {
			entry.Revision = *rev.Revision
		}
		if rev.SynthesizerGeneration != nil {
			entry.SynthesizerGeneration = *rev.SynthesizerGeneration
		}
		if rev.CompositionGeneration != nil {
			entry.CompositionGeneration = *rev.CompositionGeneration
		}
		mismatched = append(mismatched, entry)
	}
	return mismatched
}
