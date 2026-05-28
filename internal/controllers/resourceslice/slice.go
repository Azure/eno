package resourceslice

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
)

const (
	reasonAllResourcesHealthy = "AllResourcesHealthy"
	reasonNotAllApplied       = "NotAllApplied"
	reasonNotAllReady         = "NotAllReady"
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
	logger = logger.WithValues("compositionGeneration", comp.Generation, "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesisUUID", comp.Status.GetCurrentSynthesisUUID(), "synthesizerName", comp.Spec.Synthesizer.Name,
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
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
				logger.Info("resource slice is missing, ignoring because composition is being deleted", "resourceSliceName", ref.Name)
				continue
			}
			return s.handleMissingSlice(ctx, comp, ref.Name)
		}
		if err != nil {
			logger.Error(err, "failed to get resource slice")
			return ctrl.Result{}, err
		}

		// All active slices should be orphaned if composition deletion was caused by symphony deletion
		if comp.Labels != nil && comp.Labels["eno.azure.io/symphony-deleting"] == "true" {
			continue
		}
		// collect the state of every resource. Drive from spec.Resource so resources whose status has not been written yet are correctly
		// surfaced as "not applied" / not ready
		for i := range slice.Spec.Resources {
			var state apiv1.ResourceState
			if i < len(slice.Status.Resources) {
				state = slice.Status.Resources[i]
			}
			if resourceNotReconciled(comp, &state) {
				snapshot.Reconciled = false
				if resourceId := slice.IdentifierAt(i); resourceId != "" {
					if len(snapshot.NotApplied) < resourcesCap {
						snapshot.NotApplied = append(snapshot.NotApplied, resourceId)
					} else {
						snapshot.OverflowApplied += 1
					}
				}
			}

			if state.Ready == nil {
				snapshot.Ready = false
				if resourceId := slice.IdentifierAt(i); resourceId != "" {
					if len(snapshot.NotReady) < resourcesCap {
						snapshot.NotReady = append(snapshot.NotReady, resourceId)
					} else {
						snapshot.OverflowReady += 1
					}
				}
			}

			if state.Ready != nil && (snapshot.ReadyTime == nil || state.Ready.After(snapshot.ReadyTime.Time)) {
				snapshot.ReadyTime = state.Ready
			}

			if e := state.ReconciliationError; e != nil && (snapshot.Error == "" || *e > snapshot.Error) {
				snapshot.Error = *e
			}
		}
	}

	// Sort the strings so that it is order-stable
	sort.Strings(snapshot.NotApplied)
	sort.Strings(snapshot.NotReady)

	// Aggregate the status of all slices into the composition
	if !processCompositionTransition(ctx, comp, snapshot) {
		return ctrl.Result{}, nil
	}
	err = s.client.Status().Update(ctx, comp)
	if errors.IsConflict(err) {
		logger.Error(err, "Failed to update composition satus due to conflict")
		return ctrl.Result{}, fmt.Errorf("conflict while updating composition status to reflect resource slices - will retry")
	}
	if err != nil {
		logger.Error(err, "failed to update composition status")
		return ctrl.Result{}, err
	}
	logger.Info("aggregated resource status into composition", "reconciled", snapshot.Reconciled, "ready", snapshot.Ready)

	return ctrl.Result{}, nil
}

func (s *sliceController) handleMissingSlice(ctx context.Context, comp *apiv1.Composition, sliceName string) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", sliceName)

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
		logger.Info("resource slice is not missing!", "resourceSliceName", sliceName)
		return ctrl.Result{}, nil
	}
	if !errors.IsNotFound(err) {
		logger.Error(err, "failed to get resourceslice")
		return ctrl.Result{}, fmt.Errorf("getting resource slice metadata: %w", err)
	}

	// Resynthesis is required
	logger.Info("resource slice is missing - resynthesizing")
	comp.ForceResynthesis()
	err = s.client.Update(ctx, comp)
	if err != nil {
		logger.Error(err, "failed to update composition")
		return ctrl.Result{}, fmt.Errorf("updating composition pending resynthesis: %w", err)
	}
	return ctrl.Result{}, nil
}

func processCompositionTransition(ctx context.Context, comp *apiv1.Composition, snapshot statusSnapshot) (modified bool) {
	logger := logr.FromContextOrDiscard(ctx)

	appliedMsg := formatBlockingMessages(snapshot.NotApplied, snapshot.OverflowApplied)
	readyMsg := formatBlockingMessages(snapshot.NotReady, snapshot.OverflowReady)

	if snapshot.synthesisMatches(comp, appliedMsg, readyMsg) && snapshot.errorsMatch(comp) {
		logger.Info("no composition status change detected", "snapshotReconciled", snapshot.Reconciled, "snapshotReady", snapshot.Ready, "snapshotError", snapshot.Error)
		return false // either no change or no synthesis yet
	}

	// The composition's simplified error message is owned by this controller while the synthesis is being reconciled
	if comp.Status.Simplified != nil && comp.Status.Simplified.Status == "Reconciling" {
		logger.Info("updating composition simplified error", "previousError", comp.Status.Simplified.Error, "newError", snapshot.Error)
		comp.Status.Simplified.Error = snapshot.Error
	}

	if comp.Status.CurrentSynthesis == nil {
		logger.Info("no current synthesis, returning early")
		return true // nothing else to do
	}

	// Empty compositions should logically become ready immediately after reconciliation
	if len(comp.Status.CurrentSynthesis.ResourceSlices) == 0 {
		logger.Info("empty composition, setting ready time to reconciled time")
		snapshot.ReadyTime = comp.Status.CurrentSynthesis.Reconciled
	}

	now := metav1.Now()
	comp.Status.CurrentSynthesis.Reconciled = snapshot.GetReconciled(comp, &now, logger)
	comp.Status.CurrentSynthesis.Ready = snapshot.GetReady(comp, logger)
	observedGen := comp.Status.CurrentSynthesis.ObservedCompositionGeneration
	meta.SetStatusCondition(
		&comp.Status.CurrentSynthesis.Conditions,
		buildResourceConditions(apiv1.ConditionResourcesApplied, snapshot.Reconciled, snapshot.NotApplied, snapshot.OverflowApplied, observedGen),
	)
	meta.SetStatusCondition(
		&comp.Status.CurrentSynthesis.Conditions,
		buildResourceConditions(apiv1.ConditionResourcesReady, snapshot.Ready, snapshot.NotReady, snapshot.OverflowReady, observedGen),
	)
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

const resourcesCap = 25

type statusSnapshot struct {
	Reconciled bool
	Ready      bool
	ReadyTime  *metav1.Time
	Error      string

	// NotApplied is a bounded list of "kind/Name" identifiers for resources that have not yet been reconciled
	// capped at resourcesCap; if there are more resources blocking. the overflow resource applied will track the count beyong the cap
	NotApplied      []string
	NotReady        []string
	OverflowApplied int
	OverflowReady   int
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

	logger.V(1).Info("composition was reconciled")
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

	logger.V(1).Info("composition became ready")
	return s.ReadyTime
}

func formatBlockingMessages(blockingResources []string, overflow int) string {
	if len(blockingResources) == 0 {
		return ""
	}
	msg := strings.Join(blockingResources, ", ")
	if overflow > 0 {
		msg += fmt.Sprintf(", +%d more", overflow)
	}
	return msg
}

func buildResourceConditions(condType string, ok bool, blockingResources []string, overflow int, observedGen int64) metav1.Condition {
	condition := metav1.Condition{
		Type:               condType,
		ObservedGeneration: observedGen,
	}

	if ok {
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonAllResourcesHealthy
		return condition
	}

	condition.Status = metav1.ConditionFalse
	switch condType {
	case apiv1.ConditionResourcesApplied:
		condition.Reason = reasonNotAllApplied
	case apiv1.ConditionResourcesReady:
		condition.Reason = reasonNotAllReady
	}
	condition.Message = formatBlockingMessages(blockingResources, overflow)
	return condition
}

// conditionMatches returns true iff the named condition already exists with
// the desired status and message. A missing condition never matches — that
// forces a write to seed it (important on the first reconcile after rollout
// for already-healthy compositions).
func conditionMatches(comp *apiv1.Composition, t string, wantTrue bool, wantMsg string) bool {
	if comp.Status.CurrentSynthesis == nil {
		return true
	}

	condition := meta.FindStatusCondition(comp.Status.CurrentSynthesis.Conditions, t)
	if condition == nil {
		return false
	}
	isTrue := condition.Status == metav1.ConditionTrue
	return isTrue == wantTrue && condition.Message == wantMsg
}

// synthesisMatches returns true when the composition's currentSynthesis
// already reflects this snapshot's Reconciled/Ready outcome (including the
// derived conditions). When CurrentSynthesis is nil there is nothing to
// compare against, so we treat it as a match — downstream guards in
// processCompositionTransition handle that case.
func (s *statusSnapshot) synthesisMatches(comp *apiv1.Composition, appliedMsg, readyMsg string) bool {
	syn := comp.Status.CurrentSynthesis
	if syn == nil {
		return true
	}
	if (syn.Reconciled != nil) != s.Reconciled || (syn.Ready != nil) != s.Ready {
		return false
	}
	if !conditionMatches(comp, apiv1.ConditionResourcesApplied, s.Reconciled, appliedMsg) {
		return false
	}
	if !conditionMatches(comp, apiv1.ConditionResourcesReady, s.Ready, readyMsg) {
		return false
	}
	return true
}

// errorsMatch returns true when the composition's simplified error message
// (owned by this controller while the synthesis is being reconciled) already
// reflects snapshot.Error. When Simplified is nil or not in the Reconciling
// phase this controller does not own the field, so we treat it as a match.
func (s *statusSnapshot) errorsMatch(comp *apiv1.Composition) bool {
	simp := comp.Status.Simplified
	if simp == nil || simp.Status != "Reconciling" {
		return true
	}
	return simp.Error == s.Error
}
