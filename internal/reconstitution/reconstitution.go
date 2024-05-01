package reconstitution

import (
	"context"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Resource = resource.Resource

// Reconciler is implemented by types that can reconcile individual, reconstituted resources.
type Reconciler interface {
	Reconcile(ctx context.Context, req *Request) (ctrl.Result, error)
}

// Client provides read/write access to a collection of reconstituted resources.
type Client interface {
	Get(ctx context.Context, syn *SynthesisRef, res *ManifestRef) (*resource.Resource, bool)
	ListPreviousReadinessGroup(ctx context.Context, syn *SynthesisRef, group uint8) []ManifestRef
	ListNextReadinessGroup(ctx context.Context, syn *SynthesisRef, group uint8) []ManifestRef
}

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

func (m *ManifestRef) FindStatus(slice *apiv1.ResourceSlice) *apiv1.ResourceState {
	if len(slice.Status.Resources) <= m.Index {
		return nil
	}
	state := slice.Status.Resources[m.Index]
	return &state
}

// SynthesisRef refers to a specific synthesis of a composition.
type SynthesisRef struct {
	CompositionName, Namespace, UUID string
}

func NewSynthesisRef(comp *apiv1.Composition) *SynthesisRef {
	c := &SynthesisRef{CompositionName: comp.Name, Namespace: comp.Namespace}
	if comp.Status.CurrentSynthesis != nil {
		c.UUID = comp.Status.CurrentSynthesis.UUID
	}
	return c
}

// Request is like controller-runtime reconcile.Request but for reconstituted resources.
// https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile#Request
type Request struct {
	Manifest    ManifestRef
	Composition types.NamespacedName
}

// New creates a new reconstitution controller, which is responsible for "reconstituting" resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (ResourceSlice).
func New(mgr ctrl.Manager, cache *Cache, rec Reconciler) (workqueue.RateLimitingInterface, error) {
	ctrl, err := newController(mgr, cache)
	if err != nil {
		return nil, err
	}

	qp := &queueProcessor{
		Queue:   ctrl.queue,
		Handler: rec,
		Logger:  mgr.GetLogger().WithValues("controller", "reconciliationController"),
	}
	if err := mgr.Add(qp); err != nil {
		return nil, err
	}

	return ctrl.queue, nil
}
