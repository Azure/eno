package reconstitution

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
)

type StatusPatchFn func(*apiv1.ResourceState) bool

type Reconciler interface {
	Name() string
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

type Client interface {
	Get(ctx context.Context, ref *ResourceRef, gen int64) (*Resource, bool)
	PatchStatusAsync(ctx context.Context, req *ManifestRef, patchFn StatusPatchFn)
}

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	Ref *ResourceRef

	Manifest          string
	ReconcileInterval time.Duration
	object            *unstructured.Unstructured
}

func (r *Resource) Object() *unstructured.Unstructured {
	// don't allow callers to mutate the original
	return r.object.DeepCopy()
}

// ResourceRef refers to a specific synthesized resource.
type ResourceRef struct {
	Composition           types.NamespacedName
	Name, Namespace, Kind string
}

// Request is like controller-runtime reconcile.Request but for reconstituted resources.
// https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile#Request
type Request struct {
	ResourceRef
	Manifest ManifestRef
}
