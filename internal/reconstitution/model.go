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

	Manifest          string
	Object            *unstructured.Unstructured
	ReconcileInterval time.Duration
}

type Request struct {
	ResourceMeta
	Composition types.NamespacedName
	Slice       ResourceSliceRef
}

type ResourceSliceRef struct {
	SliceResource types.NamespacedName
	ResourceIndex int
}

type resourceKey struct {
	ResourceMeta
	CompositionGeneration int64
}

type synthesisKey struct {
	Namespace, Name       string
	CompositionGeneration int64
}
