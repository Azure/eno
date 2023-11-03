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
	Composition    types.NamespacedName
	SlicedResource SlicedResourceRef
}

// SlicedResourceRef references a particular resource within a resource slice.
type SlicedResourceRef struct {
	SliceResource types.NamespacedName
	ResourceIndex int // position of this resource in the slice's Resources array
}

type resourceKey struct {
	ResourceRef
	CompositionGeneration int64
}

type synthesisKey struct {
	Namespace, Name       string
	CompositionGeneration int64
}
