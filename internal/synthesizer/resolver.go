package synthesizer

import (
	"context"
	"errors"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Sentinel errors for synthesizer resolution.
var (
	// ErrNilRef is returned when a nil SynthesizerRef is provided.
	ErrNilRef = errors.New("synthesizer ref cannot be nil")

	// ErrEmptyName is returned when the synthesizer ref name is empty.
	ErrEmptyName = errors.New("synthesizer ref name is empty")

	// ErrNoMatchingSelector is returned when no synthesizers match the label selector.
	ErrNoMatchingSelector = errors.New("no synthesizers match the label selector")

	// ErrMultipleMatches is returned when more than one synthesizer matches the label selector.
	ErrMultipleMatches = errors.New("multiple synthesizers match the label selector")
)

// ResolveSynthesizer resolves a SynthesizerRef to a concrete Synthesizer.
//
// Precedence behavior: When both Name and LabelSelector are set in the ref,
// LabelSelector takes precedence and Name is ignored. This allows for more
// flexible matching when needed while maintaining backwards compatibility
// with name-based resolution.
//
// If the ref has a labelSelector, it lists all synthesizers matching the selector.
// Exactly one synthesizer must match; if zero match, ErrNoMatchingSelector is returned,
// and if more than one match, ErrMultipleMatches is returned.
//
// If labelSelector is not set, it uses the name field to get the synthesizer directly.
//
// Returns:
//   - The resolved Synthesizer if found
//   - nil, ErrNoMatchingSelector if no synthesizers match the label selector
//   - nil, ErrMultipleMatches if more than one synthesizer matches the label selector
//   - nil, error if there was an error during resolution
func ResolveSynthesizer(ctx context.Context, c client.Client, ref *apiv1.SynthesizerRef) (*apiv1.Synthesizer, error) {
	if ref == nil {
		return nil, ErrNilRef
	}

	// LabelSelector takes precedence over name
	if ref.LabelSelector != nil {
		return resolveByLabel(ctx, c, ref.LabelSelector)
	}

	// Fallback to name-based resolution
	return resolveByName(ctx, c, ref.Name)
}

// resolveByLabel resolves a Synthesizer using a label selector.
// It lists all synthesizers matching the selector and returns the matching one.
// Exactly one synthesizer must match the selector.
//
// Returns:
//   - The resolved Synthesizer if exactly one matches
//   - nil, ErrNoMatchingSelector if no synthesizers match the selector
//   - nil, ErrMultipleMatches if more than one synthesizer matches the selector
//   - nil, error if there was an error during resolution
func resolveByLabel(ctx context.Context, c client.Client, labelSelector *metav1.LabelSelector) (*apiv1.Synthesizer, error) {
	// Convert metav1.LabelSelector to labels.Selector
	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return nil, fmt.Errorf("converting label selector: %w", err)
	}

	// List all synthesizers matching the selector
	synthList := &apiv1.SynthesizerList{}
	err = c.List(ctx, synthList, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing synthesizers by label selector: %w", err)
	}

	// Handle results based on match count
	switch len(synthList.Items) {
	case 0:
		return nil, ErrNoMatchingSelector
	case 1:
		return &synthList.Items[0], nil
	default:
		return nil, ErrMultipleMatches
	}
}

// resolveByName resolves a Synthesizer using its name.
//
// Returns:
//   - The resolved Synthesizer if found
//   - nil, error if there was an error during resolution
func resolveByName(ctx context.Context, c client.Client, name string) (*apiv1.Synthesizer, error) {
	if name == "" {
		return nil, ErrEmptyName
	}
	synth := &apiv1.Synthesizer{}
	synth.Name = name

	if err := c.Get(ctx, client.ObjectKeyFromObject(synth), synth); err != nil {
		return nil, fmt.Errorf("getting synthesizer by name %q: %w", name, err)
	}
	return synth, nil
}
