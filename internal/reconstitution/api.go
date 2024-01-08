package reconstitution

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
)

// TODO: Rename some of these types for clarity

// Reconciler is implemented by types that can reconcile individual, reconstituted resources.
type Reconciler interface {
	Name() string
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

// Client provides read/write access to a collection of reconstituted resources.
type Client interface {
	Get(ctx context.Context, comp *apiv1.Composition, ref *ResourceRef, gen int64) (*Resource, bool)
	PatchStatusAsync(ctx context.Context, req *ManifestRef, patchFn StatusPatchFn)
}

type StatusPatchFn func(*apiv1.ResourceState) bool

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	Ref *ResourceRef

	Manifest *apiv1.Manifest
	Object   *unstructured.Unstructured
}

// ResourceRef refers to a specific synthesized resource.
type ResourceRef struct {
	Composition           *CompositionRef
	Name, Namespace, Kind string
}

type CompositionRef struct {
	Name, Namespace string
	Generation      int64 // TODO: Remove from Get?
}

// Request is like controller-runtime reconcile.Request but for reconstituted resources.
// https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile#Request
type Request struct {
	ResourceRef
	Manifest ManifestRef
}
