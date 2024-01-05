package cleanup

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TODO: Should we limit the max number of resource slices per composition to protect etcd in the face of partial resource slice write failures?

type resourceSliceController struct {
	client client.Client
}

func NewResourceSliceController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.ResourceSlice{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToResourceSliceHandler(mgr.GetClient())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "resourceSliceCleanupController")).
		Complete(&resourceSliceController{
			client: mgr.GetClient(),
		})
}

func (c *resourceSliceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", req.Name, "resourceSliceNamespace", req.Namespace)

	slice := &apiv1.ResourceSlice{}
	err := c.client.Get(ctx, req.NamespacedName, slice)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
	}

	owner := metav1.GetControllerOf(slice)
	comp := &apiv1.Composition{}
	ownerMissing := owner == nil

	// We only get the composition if it exists
	// It shouldn't be possible that it doesn't exist, but still worth handling in case anyone creates an ad-hoc resource slice for some reason (it won't do anything tho)
	if owner != nil {
		comp.Name = owner.Name
		comp.Namespace = slice.Namespace
		err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if errors.IsNotFound(err) || comp.DeletionTimestamp != nil {
			ownerMissing = true
		}
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
		} else {
			logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)
		}
	}

	// Release the finalizer once all resources in the current slices have been deleted to make sure we don't orphan any resources.
	// Also release the finalizer if the composition somehow doesn't exist since that implies we've lost control anyway.
	if slice.DeletionTimestamp != nil || owner == nil {
		if !ownerMissing && resourcesRemain(slice) && c.synthesisReferencesSlice(comp.Status.CurrentState, slice) {
			return ctrl.Result{}, nil
		}

		if !controllerutil.RemoveFinalizer(slice, "eno.azure.io/cleanup") {
			return ctrl.Result{}, nil // nothing to do - just wait for apiserver to delete
		}
		if err := c.client.Update(ctx, slice); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}

		logger.V(0).Info("released unused resource slice")
		return ctrl.Result{}, nil
	}

	// Ignore slices during synthesis
	if !ownerMissing && (comp.Status.CurrentState == nil || !comp.Status.CurrentState.Synthesized || (comp.Status.PreviousState != nil && !comp.Status.PreviousState.Synthesized)) {
		return ctrl.Result{}, nil
	}

	// Don't delete slices that are still referenced by their owning composition
	if !ownerMissing && (c.synthesisReferencesSlice(comp.Status.CurrentState, slice) || c.synthesisReferencesSlice(comp.Status.PreviousState, slice)) {
		return ctrl.Result{}, nil
	}

	if err := c.client.Delete(ctx, slice); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting resource slice: %w", err)
	}
	logger.V(0).Info("deleted unused resource slice")
	return ctrl.Result{}, nil
}

func (c *resourceSliceController) synthesisReferencesSlice(syn *apiv1.Synthesis, slice *apiv1.ResourceSlice) bool {
	if syn == nil {
		return false
	}
	for _, ref := range syn.ResourceSlices {
		if ref.Name == slice.Name {
			return true // referenced by the composition
		}
	}
	return false
}

func resourcesRemain(slice *apiv1.ResourceSlice) bool {
	if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
		return true // status is lagging behind
	}
	for _, state := range slice.Status.Resources {
		if !state.Deleted {
			return true
		}
	}
	return false
}
