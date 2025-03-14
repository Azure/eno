package resourceslice

import (
	"context"
	"fmt"
	"slices"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type cleanupController struct {
	client        client.Client
	noCacheReader client.Reader
}

func NewCleanupController(mgr ctrl.Manager) error {
	c := &cleanupController{
		client:        mgr.GetClient(),
		noCacheReader: mgr.GetAPIReader(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.ResourceSlice{}).
		WatchesRawSource(source.Kind(mgr.GetCache(), &apiv1.Composition{}, c.newCompEventHandler())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "sliceCleanupController")).
		Complete(c)
}

func (c *cleanupController) newCompEventHandler() handler.TypedEventHandler[*apiv1.Composition, reconcile.Request] {
	fn := func(c *apiv1.Composition, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		for _, syn := range []*apiv1.Synthesis{c.Status.CurrentSynthesis, c.Status.PreviousSynthesis} {
			if syn == nil {
				continue
			}
			for _, ref := range syn.ResourceSlices {
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: ref.Name, Namespace: c.Namespace}})
			}
		}
	}
	return &handler.TypedFuncs[*apiv1.Composition, reconcile.Request]{
		CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[*apiv1.Composition], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			fn(e.Object, q)
		},
		UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[*apiv1.Composition], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			fn(e.ObjectNew, q)
			fn(e.ObjectOld, q)
		},
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[*apiv1.Composition], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if !e.DeleteStateUnknown {
				fn(e.Object, q)
			}
		},
	}
}

func (c *cleanupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", req.Name, "resourceSliceNamespace", req.Namespace)

	slice := &apiv1.ResourceSlice{}
	err := c.client.Get(ctx, req.NamespacedName, slice)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
	}
	logger = logger.WithValues("synthesisUUID", slice.Spec.SynthesisUUID)

	owner := metav1.GetControllerOf(slice)
	if owner != nil {
		logger = logger.WithValues("compositionName", owner.Name, "compositionNamespace", req.Namespace)
	}
	ctx = logr.NewContext(ctx, logger)

	if slice.DeletionTimestamp != nil {
		return c.removeFinalizer(ctx, slice, owner)
	}

	// Don't bother checking on brand new resource slices
	if delta := time.Since(slice.CreationTimestamp.Time); delta < 5*time.Second {
		return ctrl.Result{RequeueAfter: delta}, nil
	}

	del, err := c.shouldDelete(ctx, c.client, slice, owner)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking if resource slice should be deleted (cached): %w", err)
	}
	if !del {
		return ctrl.Result{}, nil // fail safe for stale cache
	}

	del, err = c.shouldDelete(ctx, c.noCacheReader, slice, owner)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking if resource slice should be deleted: %w", err)
	}
	if !del {
		return ctrl.Result{}, nil
	}

	if err := c.client.Delete(ctx, slice, &client.Preconditions{UID: &slice.UID}); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting resource slice: %w", err)
	}
	logger.V(0).Info("deleted unused resource slice", "age", time.Since(slice.CreationTimestamp.Time).Milliseconds())

	return ctrl.Result{}, nil
}

func (c *cleanupController) shouldDelete(ctx context.Context, reader client.Reader, slice *apiv1.ResourceSlice, ref *metav1.OwnerReference) (bool, error) {
	comp := &apiv1.Composition{}
	err := reader.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: slice.Namespace}, comp)
	if errors.IsNotFound(err) {
		return false, nil // let the k8s GC controller handle it
	}
	if err != nil {
		return false, fmt.Errorf("getting composition: %w", err)
	}

	// Don't delete slices that are part of an active synthesis
	if comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID == slice.Spec.SynthesisUUID {
		return false, nil
	}

	// Check resource slice references
	for _, syn := range []*apiv1.Synthesis{comp.Status.CurrentSynthesis, comp.Status.PreviousSynthesis} {
		if syn == nil {
			continue
		}
		idx := slices.IndexFunc(syn.ResourceSlices, func(ref *apiv1.ResourceSliceRef) bool {
			return ref.Name == slice.Name
		})
		if idx != -1 {
			return false, nil
		}
	}

	return true, nil
}

// removeFinalizer removes the finalizer from the resource slice if the slice is not needed for deletion of the composition.
// The finalizer exists only to handle a case where the k8s GC controller deletes the resource slices before the composition's deletion has been reconciled.
// So we can safely release finalizers in every case _except_ when the slice is referenced by the current synthesis of a deleting composition.
//
// Since order of informer events isn't guaranteed, it's safer to avoid checking the deletion status of the composition.
// Instead, we check if the slice is part of the current synthesis of a composition that is being deleted.
// If the composition is not being deleted, holding the finalizer until reconciliation has completed is not a bad idea anyway.
func (c *cleanupController) removeFinalizer(ctx context.Context, slice *apiv1.ResourceSlice, ref *metav1.OwnerReference) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	if len(slice.Finalizers) == 0 {
		return ctrl.Result{}, nil // shouldn't be possible
	}

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: slice.Namespace}, comp)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
	}

	syn := comp.Status.CurrentSynthesis
	if syn != nil && syn.Reconciled == nil {
		idx := slices.IndexFunc(syn.ResourceSlices, func(ref *apiv1.ResourceSliceRef) bool {
			return ref.Name == slice.Name
		})
		if idx != -1 {
			return ctrl.Result{}, err // slice is needed for cleanup
		}
	}

	// It's important to not update the whole slice resource, since our informer cached representation is missing fields (to save memory)
	patchJSON := []byte(`[{"op": "remove", "path": "/metadata/finalizers"}]`)
	if err := c.client.Patch(ctx, slice, client.RawPatch(types.JSONPatchType, patchJSON)); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	logger.V(0).Info("removed resource slice finalizers")

	return ctrl.Result{}, nil
}
