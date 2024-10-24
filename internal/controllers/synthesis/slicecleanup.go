package synthesis

import (
	"context"
	"fmt"
	"reflect"
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
	client        client.Client
	noCacheReader client.Reader
}

func NewSliceCleanupController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.ResourceSlice{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToResourceSliceHandler(mgr.GetClient())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "resourceSliceCleanupController")).
		Complete(&sliceCleanupController{
			client:        mgr.GetClient(),
			noCacheReader: mgr.GetAPIReader(),
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
	if owner != nil {
		logger = logger.WithValues("compositionName", owner.Name, "compositionNamespace", req.Namespace)
	}

	decision, err := c.buildCleanupDecision(ctx, slice, owner)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building cleanup decision: %w", err)
	}
	if decision.DeferBy != nil {
		return ctrl.Result{RequeueAfter: *decision.DeferBy}, nil
	}

	if slice.DeletionTimestamp != nil || owner == nil {
		if decision.HoldFinalizer {
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
	if decision.DoNotDelete {
		return ctrl.Result{}, nil
	}

	if err := c.client.Delete(ctx, slice); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting resource slice: %w", err)
	}
	logger.V(0).Info("deleted unused resource slice")
	return ctrl.Result{}, nil
}

func (c *sliceCleanupController) buildCleanupDecision(ctx context.Context, slice *apiv1.ResourceSlice, owner *metav1.OwnerReference) (cleanupDecision, error) {
	logger := logr.FromContextOrDiscard(ctx)
	if owner == nil {
		logger.V(1).Info("resource slice can be deleted because it does not have an owner")
		return cleanupDecision{}, nil // delete
	}

	// Bail out early if the cache suggests that we shouldn't delete the resource slice
	informerDecision, err := checkCompositionState(ctx, c.client, slice, owner)
	if err != nil {
		return cleanupDecision{}, err
	}
	if informerDecision.DoNotDelete && informerDecision.HoldFinalizer {
		return informerDecision, nil
	}

	// Don't run the actual (non-cached) check if the resource is too new - the cache is probably just stale
	age := time.Since(slice.CreationTimestamp.Time)
	if age < time.Second*5 {
		logger.V(1).Info("refusing to delete resource slice because it's too new", "age", age.Milliseconds())
		return cleanupDecision{DeferBy: &age}, nil
	}

	// Check the state against apiserver without any caching before making a final decision
	apiDecision, err := checkCompositionState(ctx, c.noCacheReader, slice, owner)
	if err != nil {
		return cleanupDecision{}, err
	}
	if !reflect.DeepEqual(informerDecision, apiDecision) {
		// We trust the apiserver decision even if it doesn't align with the cache,
		// although this is should rarely happen and might be reason for concern
		logger.Info("cleanup decisions derived from informer cache and non-caching client do not agree!", "cache", informerDecision.String(), "noCache", apiDecision.String())
	}

	return apiDecision, nil
}

func checkCompositionState(ctx context.Context, reader client.Reader, slice *apiv1.ResourceSlice, owner *metav1.OwnerReference) (cleanupDecision, error) {
	comp := &apiv1.Composition{}
	comp.Name = owner.Name
	comp.Namespace = slice.Namespace
	err := reader.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if errors.IsNotFound(err) {
		return cleanupDecision{}, nil // delete
	}
	if err != nil {
		return cleanupDecision{}, fmt.Errorf("getting composition: %w", err)
	}
	return cleanupDecision{
		DoNotDelete:   !shouldDeleteSlice(comp, slice),
		HoldFinalizer: !shouldReleaseSliceFinalizer(comp, slice),
	}, nil
}

type cleanupDecision struct {
	DoNotDelete   bool
	HoldFinalizer bool
	DeferBy       *time.Duration
}

func (c *cleanupDecision) String() string {
	return fmt.Sprintf("DoNotDelete=%t,HoldFinalizer=%t", c.DoNotDelete, c.HoldFinalizer)
}

func shouldDeleteSlice(comp *apiv1.Composition, slice *apiv1.ResourceSlice) bool {
	if comp.Status.CurrentSynthesis == nil || slice.Spec.CompositionGeneration > comp.Status.CurrentSynthesis.ObservedCompositionGeneration {
		return false // stale informer
	}

	var (
		hasBeenRetried     = slice.Spec.Attempt != 0 && comp.Status.CurrentSynthesis.Attempts > slice.Spec.Attempt && slice.Spec.SynthesisUUID == comp.Status.CurrentSynthesis.UUID
		isReferencedByComp = synthesisReferencesSlice(comp.Status.CurrentSynthesis, slice) || synthesisReferencesSlice(comp.Status.PreviousSynthesis, slice)
		isSynthesized      = comp.Status.CurrentSynthesis.Synthesized != nil
		compIsDeleted      = comp.DeletionTimestamp != nil
		fromOldComposition = slice.Spec.CompositionGeneration < comp.Status.CurrentSynthesis.ObservedCompositionGeneration
	)

	// We can only safely delete resource slices when either:
	// - Another retry of the same synthesis has already started
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
