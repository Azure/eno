package inputs

import (
	"slices"

	apiv1 "github.com/Azure/eno/api/v1"
)

// Exist returns true when all of the inputs required by a synthesizer are represented by the given composition's status.
func Exist(syn *apiv1.Synthesizer, c *apiv1.Composition) bool {
	refs := map[string]struct{}{}
	for _, ref := range syn.Spec.Refs {
		refs[ref.Key] = struct{}{}
	}

	bound := map[string]struct{}{}
	for _, binding := range c.Spec.Bindings {
		if _, ok := refs[binding.Key]; !ok {
			// Ignore missing resources if the synthesizer doesn't require them
			// This is important for forwards compatibility- compositions can bind to refs that don't exist, but will in future synths
			continue
		}
		found := slices.ContainsFunc(c.Status.InputRevisions, func(rev apiv1.InputRevisions) bool {
			return binding.Key == rev.Key
		})
		if !found {
			return false
		}
		bound[binding.Key] = struct{}{}
	}

	for _, ref := range syn.Spec.Refs {
		// Handle missing resources for implied bindings
		if ref.Resource.Name != "" {
			found := slices.ContainsFunc(c.Status.InputRevisions, func(rev apiv1.InputRevisions) bool {
				return ref.Key == rev.Key
			})
			if !found {
				return false
			}
			continue
		}

		// Every ref must be bound
		if _, ok := bound[ref.Key]; !ok {
			return false
		}
	}

	return true
}

// OutOfLockstep returns true when one or more inputs that specify a revision do not match the others.
// It also returns true if any revision is derived from a synthesizer generation
// older than the provided synthesizer.
func OutOfLockstep(synth *apiv1.Synthesizer, revs []apiv1.InputRevisions) bool {
	// First, the the max revision across all bindings
	var maxRevision *int
	for _, rev := range revs {
		if rev.SynthesizerGeneration != nil && *rev.SynthesizerGeneration < synth.Generation {
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
