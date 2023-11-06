package reconstitution

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
)

var ErrNotFound = errors.New("resource not found")

type Reconciler interface {
	Name() string
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

type Client interface {
	Get(gen int64, req *ResourceRef) (*Resource, bool)
	PatchStatusAsync(ctx context.Context, req *ManifestRef, patchFn StatusPatchFn)
}

type StatusPatchFn func(*apiv1.ResourceState) bool

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
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}
