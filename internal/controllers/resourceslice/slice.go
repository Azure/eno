package resourceslice

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
)

// sliceController manages the lifecycle of resource slices in the context of their owning composition.
// This consists of aggregating their status into the composition, and replacing missing slices.
// Deletion of slices is handled by a separate controller to handle cases where the related composition no longer exists.
type sliceController struct {
	client client.Client
}

func NewController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "sliceController")).
		Complete(&sliceController{client: mgr.GetClient()})
}

func (s *sliceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := s.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger = logger.WithValues("compositionGeneration", comp.Generation, "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesisID", comp.Status.GetCurrentSynthesisUUID())
	ctx = logr.NewContext(ctx, logger)

	if comp.Status.CurrentSynthesis == nil {
		return ctrl.Result{}, nil
	}

	snapshot := statusSnapshot{Reconciled: true, Ready: true}

	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := s.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			if comp.DeletionTimestamp != nil {
				logger.V(1).Info("resource slice is missing, ignoring because composition is being deleted", "resourceSliceName", ref.Name)
				continue
			}
			return s.handleMissingSlice(ctx, comp, ref.Name)
		}
		if err != nil {
			logger.Error(err, "failed to get resource slice")
			return ctrl.Result{}, err
		}

		// Handle a case where the reconciliation controller hasn't updated the slice's status yet
		if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
			snapshot.Ready = false
			snapshot.Reconciled = false
			break // no need to check the other slices
		}

		// Collect the state of every resource
		for _, state := range slice.Status.Resources {
			state := state
			if resourceNotReconciled(comp, &state) {
				snapshot.Reconciled = false
			}
			if state.Ready == nil {
				snapshot.Ready = false
			}
			if state.Ready != nil && (snapshot.ReadyTime == nil || state.Ready.After(snapshot.ReadyTime.Time)) {
				snapshot.ReadyTime = state.Ready
			}
		}
	}

	// Aggregate the status of all slices into the composition
	if !processCompositionTransition(ctx, comp, snapshot) {
		return ctrl.Result{}, nil
	}
	err = s.client.Status().Update(ctx, comp)
	if err != nil {
		logger.Error(err, "failed to update composition status")
		return ctrl.Result{}, err
	}
	logger.V(1).Info("aggregated resource status into composition", "reconciled", snapshot.Reconciled, "ready", snapshot.Ready)

	return ctrl.Result{}, nil
}

func (s *sliceController) handleMissingSlice(ctx context.Context, comp *apiv1.Composition, sliceName string) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// We can't do anything about missing resource slices if synthesis is already in-flight or it isn't safe to resynthesize
	if comp.ShouldIgnoreSideEffects() || comp.Status.InFlightSynthesis != nil || comp.ShouldForceResynthesis() {
		return ctrl.Result{}, nil
	}

	// It's possible that newly created slices haven't hit the informer cache yet
	if synthd := comp.Status.CurrentSynthesis.Synthesized; synthd != nil {
		delta := time.Since(synthd.Time)
		if delta < time.Second*5 {
			return ctrl.Result{RequeueAfter: delta}, nil
		}
	}

	// Be absolutely sure the slice is missing
	meta := &metav1.PartialObjectMetadata{}
	meta.Kind = "ResourceSlice"
	meta.APIVersion = apiv1.SchemeGroupVersion.String()
	meta.Name = sliceName
	meta.Namespace = comp.Namespace
	err := s.client.Get(ctx, client.ObjectKeyFromObject(meta), meta)
	if err == nil {
		logger.V(1).Info("resource slice is not missing!", "resourceSliceName", sliceName)
		return ctrl.Result{}, nil
	}
	if !errors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("getting resource slice metadata: %w", err)
	}

	// Resynthesis is required
	logger.Info("resource slice is missing - resynthesizing", "resourceSliceName", sliceName)
	comp.ForceResynthesis()
	err = s.client.Update(ctx, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition pending resynthesis: %w", err)
	}
	return ctrl.Result{}, nil
}

func processCompositionTransition(ctx context.Context, comp *apiv1.Composition, snapshot statusSnapshot) (modified bool) {
	logger := logr.FromContextOrDiscard(ctx)

	if comp.Status.CurrentSynthesis == nil || ((comp.Status.CurrentSynthesis.Reconciled != nil) == snapshot.Reconciled && (comp.Status.CurrentSynthesis.Ready != nil) == snapshot.Ready) {
		return false // either no change or no synthesis yet
	}

	// Empty compositions should logically become ready immediately after reconciliation
	if len(comp.Status.CurrentSynthesis.ResourceSlices) == 0 {
		snapshot.ReadyTime = comp.Status.CurrentSynthesis.Reconciled
	}

	now := metav1.Now()
	comp.Status.CurrentSynthesis.Reconciled = snapshot.GetReconciled(comp, &now, logger)
	comp.Status.CurrentSynthesis.Ready = snapshot.GetReady(comp, logger)
	return true
}

// resourceNotReconciled returns true when the resource has not been reconciled.
// - When its status has Reconciled == true
// - When it has been deleted and the composition has also been deleted
// - When it has been deleted and the composition is configured to orphan resources
func resourceNotReconciled(comp *apiv1.Composition, state *apiv1.ResourceState) bool {
	shouldOrphan := comp.Annotations != nil && comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan"
	return !state.Reconciled || (!state.Deleted && !shouldOrphan && comp.DeletionTimestamp != nil)
}

type statusSnapshot struct {
	Reconciled bool
	Ready      bool
	ReadyTime  *metav1.Time
}

func (s *statusSnapshot) GetReconciled(comp *apiv1.Composition, now *metav1.Time, logger logr.Logger) *metav1.Time {
	if !s.Reconciled {
		return nil
	}

	if synthed := comp.Status.CurrentSynthesis.Synthesized; synthed != nil {
		latency := now.Sub(synthed.Time)
		if latency > 0 {
			logger = logger.WithValues("latency", latency.Milliseconds())
		}
	}

	logger.V(0).Info("composition was reconciled")
	return now
}

func (s *statusSnapshot) GetReady(comp *apiv1.Composition, logger logr.Logger) *metav1.Time {
	if !s.Ready || s.ReadyTime == nil {
		return nil
	}

	if synthed := comp.Status.CurrentSynthesis.Synthesized; synthed != nil {
		latency := s.ReadyTime.Sub(synthed.Time)
		if latency > 0 {
			logger = logger.WithValues("latency", latency.Milliseconds())
		}
	}

	logger.V(0).Info("composition became ready")
	return s.ReadyTime
}
