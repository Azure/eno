package reconstitution

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// ResourceRef refers to a specific synthesized resource.
type ResourceRef struct {
	Name, Namespace, Kind string
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	Ref *ResourceRef

	Manifest          string
	Object            *unstructured.Unstructured
	ReconcileInterval time.Duration
}

type Request struct {
	ResourceRef
	Composition types.NamespacedName
	Manifest    ManifestRef
}

func (r *Request) LogValues() []any {
	return []any{"composition", r.Composition, "resource", r.ResourceRef, "manifest", r.Manifest}
}

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	SliceResource types.NamespacedName
	Index         int // position of this manifest within the slice
}

type resourceKey struct {
	ResourceRef
	CompositionGeneration int64
}

type synthesisKey struct {
	Namespace, Name       string
	CompositionGeneration int64
}
