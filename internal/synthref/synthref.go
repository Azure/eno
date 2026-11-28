// Package synthref resolves a SynthesizerRef to the Synthesizer it refers to.
//
// A label selector takes precedence over the name and never falls back to it: a selector that matches
// no synthesizer, or more than one, is reported as missing.
package synthref

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// ErrAmbiguous is returned when a label selector matches more than one synthesizer.
var ErrAmbiguous = errors.New("synthesizer label selector matches more than one synthesizer")

var synthesizerResource = schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "synthesizers"}

// IsMissing reports whether err means the ref does not resolve to exactly one synthesizer.
func IsMissing(err error) bool {
	return apierrors.IsNotFound(err) || errors.Is(err, ErrAmbiguous)
}

// Resolve returns the synthesizer selected by ref.
func Resolve(ctx context.Context, cli client.Reader, ref *apiv1.SynthesizerRef) (*apiv1.Synthesizer, error) {
	if ref.LabelSelector == nil {
		synth := &apiv1.Synthesizer{}
		if err := cli.Get(ctx, types.NamespacedName{Name: ref.Name}, synth); err != nil {
			return nil, err
		}
		return synth, nil
	}

	sel, err := metav1.LabelSelectorAsSelector(ref.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing synthesizer label selector: %w", err)
	}
	list := &apiv1.SynthesizerList{}
	if err := cli.List(ctx, list, client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return nil, err
	}
	return pick(ref, list.Items)
}

// ResolveFromMap is Resolve over synthesizers that were already listed, keyed by name.
func ResolveFromMap(ref *apiv1.SynthesizerRef, synths map[string]apiv1.Synthesizer) (*apiv1.Synthesizer, error) {
	if ref.LabelSelector == nil {
		synth, ok := synths[ref.Name]
		if !ok {
			return nil, apierrors.NewNotFound(synthesizerResource, ref.Name)
		}
		return &synth, nil
	}

	var matches []apiv1.Synthesizer
	for _, synth := range synths {
		if Matches(ref, &synth) {
			matches = append(matches, synth)
		}
	}
	return pick(ref, matches)
}

// Matches reports whether ref selects synth.
func Matches(ref *apiv1.SynthesizerRef, synth *apiv1.Synthesizer) bool {
	if ref.LabelSelector == nil {
		return ref.Name == synth.Name
	}
	sel, err := metav1.LabelSelectorAsSelector(ref.LabelSelector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(synth.Labels))
}

// ListCompositions returns the compositions whose SynthesizerRef selects synth. index must key
// compositions by keyFn(manager.SynthesizerIndexKey(comp)).
func ListCompositions(ctx context.Context, cli client.Reader, synth *apiv1.Synthesizer, index string, keyFn func(synthKey string) string) ([]apiv1.Composition, error) {
	byName := &apiv1.CompositionList{}
	if err := cli.List(ctx, byName, client.MatchingFields{index: keyFn(synth.Name)}); err != nil {
		return nil, err
	}

	bySelector := &apiv1.CompositionList{}
	if err := cli.List(ctx, bySelector, client.MatchingFields{index: keyFn(manager.SelectorSynthesizerKey)}); err != nil {
		return nil, err
	}

	comps := byName.Items
	for i := range bySelector.Items {
		if Matches(&bySelector.Items[i].Spec.Synthesizer, synth) {
			comps = append(comps, bySelector.Items[i])
		}
	}
	return comps, nil
}

// Identity is the keyFn for indexes keyed by the synthesizer key alone.
func Identity(synthKey string) string { return synthKey }

func pick(ref *apiv1.SynthesizerRef, matches []apiv1.Synthesizer) (*apiv1.Synthesizer, error) {
	switch len(matches) {
	case 0:
		return nil, apierrors.NewNotFound(synthesizerResource, metav1.FormatLabelSelector(ref.LabelSelector))
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrAmbiguous, metav1.FormatLabelSelector(ref.LabelSelector))
	}
}
