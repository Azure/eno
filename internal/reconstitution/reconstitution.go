package reconstitution

import (
	"context"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Reconciler is implemented by types that can reconcile individual, reconstituted resources.
type Reconciler interface {
	Reconcile(ctx context.Context, req *resource.Request) (ctrl.Result, error)
}

type RangeDirection bool

var (
	RangeAsc  RangeDirection = true
	RangeDesc RangeDirection = false
)

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

// New creates a new reconstitution controller, which is responsible for "reconstituting" resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (ResourceSlice).
func New(mgr ctrl.Manager, cache *resource.Cache, rec Reconciler) error {
	_, err := newController(mgr, cache)
	if err != nil {
		return err
	}

	qp := &queueProcessor{
		Queue:   cache.Queue,
		Handler: rec,
		Logger:  mgr.GetLogger().WithValues("controller", "reconciliationController"),
	}
	return mgr.Add(qp)
}
