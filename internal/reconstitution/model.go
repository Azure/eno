package reconstitution

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type GeneratedResourceMeta struct {
	Name, Namespace, Kind string
}

// GeneratedResource is the controller's internal representation of a single generated resource out of a GeneratedResourceSlice.
type GeneratedResource struct {
	Meta   *GeneratedResourceMeta
	Spec   *GeneratedResourceSpec
	Status *GeneratedResourceStatus
}

type GeneratedResourceSpec struct {
	Manifest string
	Object   *unstructured.Unstructured

	ReconcileInterval time.Duration
}

type GeneratedResourceStatus struct {
	Synced                  bool
	ObservedResourceVersion string
}

type Request struct {
	GeneratedResourceMeta
	Generation types.NamespacedName
}

type resourceKey struct {
	Namespace, Name, Kind string
	GenerationGeneration  int64 // metadata.generation of the parent Generation resource
}

func newResourceKey(gen int64, gr *GeneratedResource) resourceKey {
	return resourceKey{
		Namespace:            gr.Meta.Namespace,
		Name:                 gr.Meta.Name,
		Kind:                 gr.Meta.Kind,
		GenerationGeneration: gen,
	}
}

type generationKey struct {
	Namespace, Name string
	Generation      int64
}
