package reconstitution

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type ResourceMeta struct {
	Name, Namespace, Kind string
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	Meta *ResourceMeta
	Spec *ResourceSpec
}

type ResourceSpec struct {
	Manifest string
	Object   *unstructured.Unstructured

	ReconcileInterval time.Duration
}

type Request struct {
	ResourceMeta
	Generation types.NamespacedName
}

type resourceKey struct {
	Namespace, Name, Kind string
	GenerationGeneration  int64 // metadata.generation of the parent Generation resource
}

func newResourceKey(gen int64, gr *Resource) resourceKey {
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
