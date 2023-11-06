package reconstitution

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/Azure/eno/api/v1"
)

type StatusPatchFn func(*apiv1.ResourceState) bool

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	Ref *ResourceRef

	Manifest          string
	Object            *unstructured.Unstructured
	ReconcileInterval time.Duration
}

// ResourceRef refers to a specific synthesized resource.
type ResourceRef struct {
	Composition           types.NamespacedName
	Name, Namespace, Kind string
}

// Request is like controller-runtime reconcile.Request but for reconstituted resources.
type Request struct {
	ResourceRef
	Manifest ManifestRef
}
