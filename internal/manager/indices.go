package manager

import (
	"path"

	apiv1 "github.com/Azure/eno/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	IdxCompositionsBySynthesizer = ".spec.synthesizer"
	IdxCompositionsBySymphony    = ".compositionsBySymphony"
	IdxCompositionsByBinding     = ".compositionsByBinding"
	IdxSynthesizersByRef         = ".synthesizersByRef"
	IdxCompositionsByDependency  = ".spec.dependsOn"

	// SelectorSynthesizerKey replaces the synthesizer name in the index keys of compositions that
	// select their synthesizer by label, since the matching synthesizer isn't known at index time.
	SelectorSynthesizerKey = "*labelSelector"
)

// SynthesizerIndexKey returns the synthesizer part of a composition's index keys.
func SynthesizerIndexKey(comp *apiv1.Composition) string {
	if comp.Spec.Synthesizer.LabelSelector != nil {
		return SelectorSynthesizerKey
	}
	return comp.Spec.Synthesizer.Name
}

func indexSynthesizer() client.IndexerFunc {
	return func(o client.Object) []string {
		comp, ok := o.(*apiv1.Composition)
		if !ok {
			return nil
		}
		return []string{SynthesizerIndexKey(comp)}
	}
}

func indexController() client.IndexerFunc {
	return func(o client.Object) []string {
		owner := metav1.GetControllerOf(o)
		if owner == nil {
			return nil
		}
		return []string{owner.Name}
	}
}

func indexResourceBindings() client.IndexerFunc {
	return func(o client.Object) []string {
		comp, ok := o.(*apiv1.Composition)
		if !ok {
			return nil
		}

		keys := []string{}
		for _, binding := range comp.Spec.Bindings {
			keys = append(keys, path.Join(SynthesizerIndexKey(comp), binding.Resource.Namespace, binding.Resource.Name))
		}
		return keys
	}
}

func indexSynthRefs() client.IndexerFunc {
	return func(o client.Object) []string {
		synth, ok := o.(*apiv1.Synthesizer)
		if !ok {
			return nil
		}

		keys := []string{}
		for _, ref := range synth.Spec.Refs {
			keys = append(keys, path.Join(ref.Resource.Group, ref.Resource.Version, ref.Resource.Kind))
		}
		return keys
	}
}

func indexCompositionsByDependency() client.IndexerFunc {
	return func(o client.Object) []string {
		comp, ok := o.(*apiv1.Composition)
		if !ok {
			return nil
		}
		var keys []string
		for _, dep := range comp.Spec.DependsOn {
			keys = append(keys, path.Join(dep.Namespace, dep.Name))
		}
		return keys
	}
}
