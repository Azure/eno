package reconstitution

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/cel"
)

// Reconciler is implemented by types that can reconcile individual, reconstituted resources.
type Reconciler interface {
	Name() string
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

// Client provides read/write access to a collection of reconstituted resources.
type Client interface {
	Get(ctx context.Context, comp *CompositionRef, res *ResourceRef) (*Resource, bool)
	PatchStatusAsync(ctx context.Context, req *ManifestRef, patchFn StatusPatchFn)
}

type StatusPatchFn func(*apiv1.ResourceState) *apiv1.ResourceState

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	lastSeenMeta
	lastReconciledMeta

	Ref             ResourceRef
	Manifest        *apiv1.Manifest
	GVK             schema.GroupVersionKind
	SliceDeleted    bool
	ReadinessChecks []*ReadinessCheck
}

type ReadinessCheck struct {
	Name string
	AST  *cel.Ast
}

func (r *Resource) Deleted() bool { return r.SliceDeleted || r.Manifest.Deleted }

func (r *Resource) Parse() (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	return u, u.UnmarshalJSON([]byte(r.Manifest.Manifest))
}

// ResourceRef refers to a specific synthesized resource.
type ResourceRef struct {
	Name, Namespace, Kind string
}

// CompositionRef refers to a specific generation of a composition.
type CompositionRef struct {
	Name, Namespace string
	Generation      int64
}

func NewCompositionRef(comp *apiv1.Composition) *CompositionRef {
	c := &CompositionRef{Name: comp.Name, Namespace: comp.Namespace}
	if comp.Status.CurrentState != nil {
		c.Generation = comp.Status.CurrentState.ObservedCompositionGeneration
	}
	return c
}

// Request is like controller-runtime reconcile.Request but for reconstituted resources.
// https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile#Request
type Request struct {
	Resource    ResourceRef
	Manifest    ManifestRef
	Composition types.NamespacedName
}

type lastSeenMeta struct {
	lock            sync.Mutex
	resourceVersion string
}

func (l *lastSeenMeta) ObserveVersion(rv string) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.resourceVersion = rv
}

func (l *lastSeenMeta) HasBeenSeen() bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.resourceVersion != ""
}

func (l *lastSeenMeta) MatchesLastSeen(rv string) bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.resourceVersion == rv
}

type lastReconciledMeta struct {
	lock           sync.Mutex
	lastReconciled *time.Time
}

func (l *lastReconciledMeta) ObserveReconciliation() time.Duration {
	now := time.Now()

	l.lock.Lock()
	defer l.lock.Unlock()

	var latency time.Duration
	if l.lastReconciled != nil {
		latency = now.Sub(*l.lastReconciled)
	}

	l.lastReconciled = &now
	return latency
}
