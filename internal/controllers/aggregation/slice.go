package aggregation

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
)

type sliceController struct {
	client client.Client
}

func NewSliceController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "sliceAggregationController")).
		Complete(&sliceController{
			client: mgr.GetClient(),
		})
}

func (s *sliceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := s.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}

	logger = logger.WithValues("compositionGeneration", comp.Generation,
		"compositionName", comp.Name,
		"compositionNamespace", comp.Namespace,
		"synthesisID", comp.Status.GetCurrentSynthesisUUID())

	if compositionStatusTerminal(comp) {
		return ctrl.Result{}, nil
	}

	var maxReadyTime *metav1.Time
	ready := true
	reconciled := true
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := s.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if comp.DeletionTimestamp != nil && errors.IsNotFound(err) {
			// It's possible for resource slices to be deleted before we have time to aggregate their status into the composition,
			// but that shouldn't break the deletion flow. Missing resource slice means its been cleaned up when the comp is deleting.
			continue
		}
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
		}

		// Status might be lagging behind
		if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
			ready = false
			reconciled = false
			break
		}

		for _, state := range slice.Status.Resources {
			state := state
			// A resource is reconciled when it's... been reconciled OR when the composition is deleting and it's been deleted.
			// One more special case: it's also been reconciled when it still exists but the composition is deleting and is configured to orphan resources.
			if resourceNotReconciled(comp, &state) {
				reconciled = false
			}

			// Readiness
			if state.Ready == nil {
				ready = false
			}
			if state.Ready != nil && (maxReadyTime == nil || maxReadyTime.Before(state.Ready)) {
				maxReadyTime = state.Ready
			}
		}
	}

	if compositionStatusInSync(comp, reconciled, ready) {
		return ctrl.Result{}, nil
	}

	now := metav1.Now()
	if ready && maxReadyTime != nil {
		comp.Status.CurrentSynthesis.Ready = maxReadyTime

		if synthed := comp.Status.CurrentSynthesis.Synthesized; synthed != nil {
			latency := maxReadyTime.Sub(synthed.Time)
			if latency.Microseconds() > 0 {
				logger.V(0).Info("composition became ready", "latency", latency.Abs().Microseconds())
			}
		}
	} else {
		comp.Status.CurrentSynthesis.Ready = nil
	}

	if reconciled {
		comp.Status.CurrentSynthesis.Reconciled = &now

		if synthed := comp.Status.CurrentSynthesis.Synthesized; synthed != nil {
			latency := now.Sub(synthed.Time)
			logger.V(0).Info("composition was reconciled", "latency", latency.Abs().Microseconds())
		}
	} else {
		comp.Status.CurrentSynthesis.Reconciled = nil
	}

	err = s.client.Status().Update(ctx, comp)
	if err != nil {
		logger.V(0).Info(" Error in aggregating resource status into composition", "msg", err)
		return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)

	}
	logger.V(0).Info("aggregated resource status into composition")

	return ctrl.Result{}, nil
}

// resourceNotReconciled returns true when a resource should be considered reconciled.
// - When its status has Reconciled == true
// - When it has been deleted and the composition has also been deleted
// - When it has been deleted and the composition is configured to orphan resources
func resourceNotReconciled(comp *apiv1.Composition, state *apiv1.ResourceState) bool {
	shouldOrphan := comp.Annotations != nil && comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan"
	return !state.Reconciled || (!state.Deleted && !shouldOrphan && comp.DeletionTimestamp != nil)
}

// compositionStatusTerminal determines if a status has reached the point that it can no longer
// progress, from the perspective of the status aggregation controller.
func compositionStatusTerminal(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil || (comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.Reconciled != nil)
}

// compositionStatusInSync compares the given bool representation of a composition's state against its current status struct.
func compositionStatusInSync(comp *apiv1.Composition, reconciled, ready bool) bool {
	return (comp.Status.CurrentSynthesis.Reconciled != nil) == reconciled && (comp.Status.CurrentSynthesis.Ready != nil) == ready
}
