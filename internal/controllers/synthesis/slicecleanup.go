package synthesis

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type sliceCleanupController struct {
	client client.Client
}

func NewSliceCleanupController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.ResourceSlice{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToResourceSliceHandler(mgr.GetClient())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "resourceSliceCleanupController")).
		Complete(&sliceCleanupController{
			client: mgr.GetClient(),
		})
}

func (c *sliceCleanupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", req.Name, "resourceSliceNamespace", req.Namespace)

	slice := &apiv1.ResourceSlice{}
	err := c.client.Get(ctx, req.NamespacedName, slice)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
	}

	owner := metav1.GetControllerOf(slice)

	// We only get the composition if it exists
	// It shouldn't be possible that it doesn't exist, but still worth handling in case anyone creates an ad-hoc resource slice for some reason (it won't do anything tho)
	var doNotDelete bool
	var holdFinalizer bool
	if owner != nil {
		comp := &apiv1.Composition{}
		comp.Name = owner.Name
		comp.Namespace = slice.Namespace
		err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if errors.IsNotFound(err) && time.Since(slice.CreationTimestamp.Time) < time.Minute {
			logger.V(1).Info("didn't find a composition for this resource slice - ignoring because resource slice is new so informer may just be stale")
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
		}
		if err == nil {
			logger = logger.WithValues("compositionName", comp.Name,
				"compositionNamespace", comp.Namespace,
				"synthesisID", comp.Status.GetCurrentSynthesisUUID())
			doNotDelete = !shouldDeleteSlice(comp, slice)
			holdFinalizer = !shouldReleaseSliceFinalizer(comp, slice)
		}
	}

	// Release the finalizer once all resources in the current slices have been deleted to make sure we don't orphan any resources.
	// Also release the finalizer if the composition somehow doesn't exist since that implies we've lost control anyway.
	if slice.DeletionTimestamp != nil || owner == nil {
		if holdFinalizer {
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

	if doNotDelete {
		return ctrl.Result{}, nil
	}
	if err := c.client.Delete(ctx, slice); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting resource slice: %w", err)
	}
	logger.V(0).Info("deleted unused resource slice")
	return ctrl.Result{}, nil
}

func shouldDeleteSlice(comp *apiv1.Composition, slice *apiv1.ResourceSlice) bool {
	if comp.Status.CurrentSynthesis == nil || slice.Spec.CompositionGeneration > comp.Status.CurrentSynthesis.ObservedCompositionGeneration {
		return false // stale informer
	}

	var (
		hasBeenRetried     = slice.Spec.Attempt != 0 && comp.Status.CurrentSynthesis.Attempts > slice.Spec.Attempt
		isReferencedByComp = synthesisReferencesSlice(comp.Status.CurrentSynthesis, slice) || synthesisReferencesSlice(comp.Status.PreviousSynthesis, slice)
		isSynthesized      = comp.Status.CurrentSynthesis.Synthesized != nil
		compIsDeleted      = comp.DeletionTimestamp != nil
		fromOldComposition = slice.Spec.CompositionGeneration < comp.Status.CurrentSynthesis.ObservedCompositionGeneration
	)

	// We can only safely delete resource slices when either:
	// - Another retry of the same synthesis has already started (TODO)
	// - Synthesis is complete and the composition is being deleted
	// - The slice was derived from an older composition
	return hasBeenRetried || (isSynthesized && compIsDeleted) || (!isReferencedByComp && fromOldComposition)
}

func shouldReleaseSliceFinalizer(comp *apiv1.Composition, slice *apiv1.ResourceSlice) bool {
	if comp.Status.CurrentSynthesis == nil || slice.Spec.CompositionGeneration > comp.Status.CurrentSynthesis.ObservedCompositionGeneration {
		return false // stale informer
	}
	isOutdated := slice.Spec.Attempt != 0 && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Attempts > slice.Spec.Attempt
	isSynthesized := comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	return isOutdated || (isSynthesized && (!resourcesRemain(comp, slice) || (!synthesisReferencesSlice(comp.Status.CurrentSynthesis, slice) && !synthesisReferencesSlice(comp.Status.PreviousSynthesis, slice))))
}

func synthesisReferencesSlice(syn *apiv1.Synthesis, slice *apiv1.ResourceSlice) bool {
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

func resourcesRemain(comp *apiv1.Composition, slice *apiv1.ResourceSlice) bool {
	if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
		return true // status is lagging behind
	}
	shouldOrphan := comp != nil && comp.Annotations != nil && comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan"
	for _, state := range slice.Status.Resources {
		if !state.Deleted && !shouldOrphan {
			return true
		}
	}
	return false
}
