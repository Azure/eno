package composition

import (
	"context"
	"fmt"
	"path"
	"sort"
	"time"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/inputs"
	"github.com/Azure/eno/internal/manager"
)

const (
	enoCompositionForceDeleteAnnotation = "eno.azure.io/forceDeleteWhenSymphonyGone"
	AKSComponentLabel                   = "aks.azure.com/component-type" // TODO(ruinanliu): Temp workaround remove after 14802391 is released
	addOnLabelValue                     = "addon"                        // TODO(ruinanliu):  Temp workaround remove after 14802391 is released
	EnoCleanupFinalizer                 = "eno.azure.io/cleanup"
	MissingInputStatus                  = "MissingInputs"
	MismatchedInputsStatus              = "MismatchedInputs"
	NotReadyStatus                      = "NotReady"
	ReconcilingStatus                   = "Reconciling"
	LogSampleCap                        = 50
)

// podCompletionGracePeriod is how long after a synthesizer pod is observed
// terminal we wait before treating its in-flight synthesis as abandoned, giving
// a late-but-successful executor status write time to propagate. It is a var so
// tests can shorten it.
var podCompletionGracePeriod = 5 * time.Second

// inFlightPollInterval bounds how often the composition controller re-checks an
// in-flight synthesis's pod phase. It lets the fast-cancel path observe a
// terminal pod promptly without watching Pods, while podTimeout remains the
// upper bound for genuinely hung pods. It is a var so tests can shorten it.
var inFlightPollInterval = 5 * time.Second

type compositionController struct {
	client                client.Client
	podTimeout            time.Duration
	synthesisPodNamespace string
}

func NewController(mgr ctrl.Manager, podTimeout time.Duration, synthesisPodNamespace string) error {
	c := &compositionController{
		client:                mgr.GetClient(),
		podTimeout:            podTimeout,
		synthesisPodNamespace: synthesisPodNamespace,
	}
	depPredicate := predicate.TypedFuncs[*apiv1.Composition]{
		CreateFunc: func(e event.TypedCreateEvent[*apiv1.Composition]) bool { return false },
		DeleteFunc: func(e event.TypedDeleteEvent[*apiv1.Composition]) bool { return true },
		UpdateFunc: func(e event.TypedUpdateEvent[*apiv1.Composition]) bool {
			// We only notify dependents if their finalizer is removed, meaning actually deleted
			prevHasFinalizer := controllerutil.ContainsFinalizer(e.ObjectOld, EnoCleanupFinalizer)
			curHasFinalizer := controllerutil.ContainsFinalizer(e.ObjectNew, EnoCleanupFinalizer)
			return prevHasFinalizer && !curHasFinalizer
		},
		GenericFunc: func(e event.TypedGenericEvent[*apiv1.Composition]) bool { return false },
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WatchesRawSource(source.Kind(mgr.GetCache(), &apiv1.Synthesizer{}, c.newSynthEventHandler())).
		WatchesRawSource(source.Kind(mgr.GetCache(), &apiv1.Composition{}, c.newDependencyEventHandler(), depPredicate)).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionController")).
		Complete(c)
}

func (c *compositionController) newDependencyEventHandler() handler.TypedEventHandler[*apiv1.Composition, reconcile.Request] {
	fn := func(ctx context.Context, comp *apiv1.Composition) (reqs []reconcile.Request) {
		// When a composition's finalizer is removed (effectively deleted),
		// notify the compositions it depends on so they can re-check
		// hasActiveDependents and unblock their own deletion.
		for _, dep := range comp.Spec.DependsOn {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      dep.Name,
					Namespace: dep.Namespace,
				},
			})
		}
		return reqs
	}
	return handler.TypedEnqueueRequestsFromMapFunc(fn)
}

func (c *compositionController) newSynthEventHandler() handler.TypedEventHandler[*apiv1.Synthesizer, reconcile.Request] {
	fn := func(ctx context.Context, synth *apiv1.Synthesizer) (reqs []reconcile.Request) {
		logger := logr.FromContextOrDiscard(ctx)

		list := &apiv1.CompositionList{}
		err := c.client.List(ctx, list, client.MatchingFields{
			manager.IdxCompositionsBySynthesizer: synth.Name,
		})
		if err != nil {
			logger.Error(err, "failed to list compositions for synthesizer")
			return nil
		}
		for _, comp := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&comp)})
		}
		return reqs
	}
	return handler.TypedEnqueueRequestsFromMapFunc(fn)
}

func (c *compositionController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisUUID", comp.Status.GetCurrentSynthesisUUID(),
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())

	if comp.DeletionTimestamp != nil {
		return c.reconcileDeletedComposition(ctx, comp)
	}

	if controllerutil.AddFinalizer(comp, EnoCleanupFinalizer) {
		err = c.client.Update(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to update composition")
			return ctrl.Result{}, err
		}
		logger.Info("added cleanup finalizer to composition")
		return ctrl.Result{}, nil
	}

	synth := &apiv1.Synthesizer{}
	synth.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(synth), synth)
	if errors.IsNotFound(err) {
		logger.Info(fmt.Sprintf("synthesizer not found for composition[%s], namespace[%s], synthName[%s]", comp.GetName(), comp.GetNamespace(), comp.Spec.Synthesizer.Name))
		synth = nil
		err = nil
	}
	if err != nil {
		logger.Error(err, "failed to get synthesizer")
		return ctrl.Result{}, err
	}
	if synth != nil {
		logger = logger.WithValues("synthesizerName", synth.Name, "synthesizerGeneration", synth.Generation)
	}
	ctx = logr.NewContext(ctx, logger)

	// Write the simplified status
	modified, err := c.reconcileSimplifiedStatus(ctx, synth, comp)
	if err != nil {
		logger.Error(err, "failed to reconcile simplified status")
		return ctrl.Result{}, err
	}
	if modified || synth == nil {
		return ctrl.Result{}, nil
	}

	// Enforce the synthesis timeout period
	if syn := comp.Status.InFlightSynthesis; syn != nil && syn.Canceled == nil && syn.Initialized != nil {
		// If the synthesizer pod already terminated because of various reasons in skipSynthesis, we
		// don't want to wait for the full cancellation, we want to fail fast and cancel early
		pod, terminal, err := c.terminalInFlightPod(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to check synthesis pod phase")
			return ctrl.Result{}, err
		}

		if terminal {
			if grace := time.Until(podTerminationTime(pod).Add(podCompletionGracePeriod)); grace > 0 {
				return ctrl.Result{RequeueAfter: grace}, nil
			}

			// A successful executor write bumps resourceVersion, so Status.Update() will conflict and we requeue. If the synthesis already advanced/cancelled do nothing
			if comp.Status.InFlightSynthesis == nil || comp.Status.InFlightSynthesis.UUID != syn.UUID ||
				comp.Status.InFlightSynthesis.Canceled != nil {
				return ctrl.Result{}, nil
			}

			comp.Status.InFlightSynthesis.Canceled = ptr.To(metav1.Now())
			if err := c.client.Status().Update(ctx, comp); err != nil {
				if errors.IsConflict(err) {
					return ctrl.Result{Requeue: true}, nil
				}
				logger.Error(err, "failed to cancel synthesis after pod terminated without advancing status")
				return ctrl.Result{}, err
			}
			logger.Info("synthesis pod terminated without advancing status - cancelling for retry",
				"podPhase", string(pod.Status.Phase),
				"podName", pod.Name,
				"synthesisUUID", syn.UUID,
				"compositionGeneration", comp.Generation,
			)
			return ctrl.Result{}, nil
		}

		// No terminal pod yet. Poll on a short interval (capped by the remaining
		// timeout) so a terminal pod is noticed promptly without watching Pods,
		// while podTimeout remains the backstop for genuinely hung pods.
		delta := time.Until(syn.Initialized.Time.Add(c.podTimeout))
		if delta > 0 {
			next := inFlightPollInterval
			if delta < next {
				next = delta
			}
			return ctrl.Result{RequeueAfter: next}, nil
		}
		syn.Canceled = ptr.To(metav1.Now())
		if err := c.client.Status().Update(ctx, comp); err != nil {
			logger.Error(err, "failed to update composition status to reflect synthesis timeout")
			return ctrl.Result{}, err
		}
		logger.Error(nil, "synthesis timed out")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (c *compositionController) reconcileDeletedComposition(ctx context.Context, comp *apiv1.Composition) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// In case in a partial cleanup, we need to check if we want to force remove finalizer before checking hasActiveDependents
	if c.shouldForceRemoveFinalizer(ctx, comp) {
		logger.Info("force removing finalizer: owning symphony is gone",
			"compositionName", comp.Name, "compositionNamespace", comp.Namespace)
		if controllerutil.RemoveFinalizer(comp, EnoCleanupFinalizer) {
			if err := c.client.Update(ctx, comp); err != nil {
				logger.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
		}
		logger.Info("removed finalizer from composition")
		return ctrl.Result{}, nil
	}

	// check whether the composition has any active dependants
	blocked, blockedBy, err := c.hasActiveDependents(ctx, comp)
	if err != nil {
		logger.Error(err, "failed to get the compositions' dependents")
		return ctrl.Result{}, err
	}

	if blocked {
		logger.Info("waiting for dependents to be deleted", "dependentCount", len(blockedBy))
		return c.updateDependencyStatus(ctx, comp, apiv1.WaitingOnDependentsReason, blockedBy)
	}

	// Once the given compositions's dependents are deleted then we can clear the dependency status
	// Note that once we clear the hasActiveDependents step we will proceed to deletion. This clear step
	// is just for the correctness  step during this breif window
	if comp.Status.DependencyStatus != nil {
		if _, err = c.clearDependencyStatus(ctx, comp); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	syn := comp.Status.CurrentSynthesis
	if syn != nil {
		// Deletion increments the composition's generation, but the reconstitution cache is only invalidated
		// when the synthesized generation (from the status) changes, which will never happen because synthesis
		// is righly disabled for deleted compositions. We break out of this deadlock condition by updating
		// the status without actually synthesizing.
		if syn.ObservedCompositionGeneration != comp.Generation {
			comp.Status.CurrentSynthesis.ObservedCompositionGeneration = comp.Generation
			comp.Status.CurrentSynthesis.UUID = uuid.NewString()
			comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
			comp.Status.CurrentSynthesis.Reconciled = nil
			comp.Status.CurrentSynthesis.Ready = nil
			err := c.client.Status().Update(ctx, comp)
			if err != nil {
				logger.Error(err, "failed to update current composition generation")
				return ctrl.Result{}, err
			}
			logger.Info("updated composition status to reflect deletion", "synthesisUUID", comp.Status.CurrentSynthesis.UUID)
			return ctrl.Result{}, nil
		}

		if syn.Reconciled == nil {
			logger.Info("refusing to remove composition finalizer because it is still being reconciled")
			return ctrl.Result{}, nil
		}
	}

	if controllerutil.RemoveFinalizer(comp, EnoCleanupFinalizer) {
		err := c.client.Update(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}

		logger.Info("removed finalizer from composition")
	}

	return ctrl.Result{}, nil
}

func (c *compositionController) reconcileSimplifiedStatus(ctx context.Context, synth *apiv1.Synthesizer, comp *apiv1.Composition) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	next := buildSimplifiedStatus(synth, comp)
	if equality.Semantic.DeepEqual(next, comp.Status.Simplified) {
		return false, nil
	}

	if synth != nil {
		switch next.Status {
		case MissingInputStatus:
			logger.Info("composition is missing required inputs", "missingInputs", inputs.Missing(synth, comp), "expectedInputs", inputs.Expected(synth))
		case MismatchedInputsStatus:
			logger.Info("composition has inputs that are out of lockstep", "mismatchedInputs", inputs.Mismatched(synth, comp, comp.Status.InputRevisions), "synthesizerGeneration", synth.Generation, "compositionGeneration", comp.Generation)
		case NotReadyStatus, ReconcilingStatus:
			c.logNotReadyResources(ctx, comp)
		}
	}

	logger.Info("composition status changed", "previousStatus", comp.Status.Simplified, "currentStatus", next)
	copy := comp.DeepCopy()
	copy.Status.Simplified = next
	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(comp)); err != nil {
		return false, fmt.Errorf("patching simplified status: %w", err)
	}
	logger.Info("sucessfully updated status for composition")
	return true, nil
}

// logNotReadyResources queries the composition's resource slices and logs the identifiers (Kind/Name) of resources that have not yet been applied/reconciled or become ready.
func (c *compositionController) logNotReadyResources(ctx context.Context, comp *apiv1.Composition) {
	logger := logr.FromContextOrDiscard(ctx)
	if comp.Status.CurrentSynthesis == nil {
		return
	}

	var notReady []string
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		if err := c.client.Get(ctx, client.ObjectKey{Namespace: comp.Namespace, Name: ref.Name}, slice); err != nil {
			logger.V(1).Info("could not get resource slice while logging not-ready resources", "resourceSliceName", ref.Name, "error", err.Error())
			continue
		}

		// Status.Resources is index-aligned with Spec.Resources, so IdentifierAt(i) names the
		// resource whose state lives at slice.Status.Resources[i].
		for i := range slice.Spec.Resources {
			// Tombstones (manifests marked for deletion) have no meaningful readiness
			// semantics, so they should not be reported as not-ready.
			if slice.Spec.Resources[i].Deleted {
				continue
			}
			id := slice.IdentifierAt(i)
			if id == "" {
				continue
			}
			var state *apiv1.ResourceState
			if i < len(slice.Status.Resources) {
				state = &slice.Status.Resources[i]
			}
			// A resource is "not ready" until it has been both reconciled (applied) and become
			// ready. Per-resource detail (why it isn't ready) is available from the
			// reconciliationController's "resource is not ready" log line.
			if state == nil || !state.Reconciled || state.Ready == nil {
				notReady = append(notReady, id)
			}
		}
	}

	if len(notReady) == 0 {
		return
	}

	// Canonical order so the logged set is stable across reconciles (and hashable if we ever
	// dedupe these lines by content).
	sort.Strings(notReady)

	// Bound the payload so a composition with thousands of resources can't emit a log line that
	// exceeds the logging backend's row-size limit; the overflow count preserves the signal.
	notReadyOverflow := 0
	if len(notReady) > LogSampleCap {
		notReadyOverflow = len(notReady) - LogSampleCap
		notReady = notReady[:LogSampleCap]
	}

	logger.Info("composition has resources that are not yet ready",
		"observedGeneration", comp.Status.CurrentSynthesis.ObservedCompositionGeneration,
		"notReady", notReady,
		"notReadyOverflow", notReadyOverflow,
	)
}

// shouldForceRemoveFinalizer returns true if and only if the composition has the
// annotation eno.azure.io/forceDeleteWhenSymphonyGone set to "true" AND the owning
// Symphony no longer exists. If the annotation is absent, not "true", or the Symphony
// still exists, this returns false.
func (c *compositionController) shouldForceRemoveFinalizer(ctx context.Context, comp *apiv1.Composition) bool {
	logger := logr.FromContextOrDiscard(ctx)

	// TODO(ruinanliu): Temp workaround remove isAddonComposition method after PR 14802391 is released
	if !isCompositionMarkedForcedDelete(comp) && !isAddonComposition(comp) {
		return false
	}

	// Find the owning Symphony from the owner references.
	ownerRefs := comp.GetOwnerReferences()
	var symphName string
	for _, ref := range ownerRefs {
		if ref.Kind == "Symphony" {
			symphName = ref.Name
			break
		}
	}
	if symphName == "" {
		logger.Info("composition has no Symphony owner reference, skip force removing finalizer",
			"compositionName", comp.GetName(), "compositionNamespace", comp.GetNamespace())
		return false
	}

	// Check if the owning Symphony still exists.
	symph := &apiv1.Symphony{}
	symphKey := types.NamespacedName{
		Namespace: comp.GetNamespace(),
		Name:      symphName,
	}
	logger.Info("checking if owning symphony still exists",
		"symphonyName", symphName, "symphonyNamespace", comp.GetNamespace())
	err := c.client.Get(ctx, symphKey, symph)
	if errors.IsNotFound(err) {
		logger.Info("owning symphony is gone, force removing finalizer",
			"compositionName", comp.GetName(), "compositionNamespace", comp.GetNamespace(),
			"symphonyName", symphName)
		return true
	}
	if err != nil {
		// Transient error — don't force remove; we'll retry on the next reconcile.
		logger.Error(err, "failed to check if owning symphony exists, skip force removing finalizer",
			"symphonyName", symphName)
		return false
	}

	logger.Info("symphony still exists, skip force removing finalizer",
		"compositionName", comp.GetName(), "symphonyName", symphName)
	return false
}

// isCompositionMarkedForcedDelete checks if a composition has the force-delete annotation set to "true".
func isCompositionMarkedForcedDelete(comp *apiv1.Composition) bool {
	annotations := comp.GetAnnotations()
	if annotations == nil {
		return false
	}
	return annotations[enoCompositionForceDeleteAnnotation] == "true"
}

// isAddonComposition checks if the composition's label contains addon label.
// TODO(ruinanliu): Temp workaround remove after PR 14802391 is released
func isAddonComposition(comp *apiv1.Composition) bool {
	labels := comp.GetLabels()
	if labels == nil {
		return false
	}
	return labels[AKSComponentLabel] == addOnLabelValue
}

func buildSimplifiedStatus(synth *apiv1.Synthesizer, comp *apiv1.Composition) *apiv1.SimplifiedStatus {
	status := &apiv1.SimplifiedStatus{}
	current := comp.Status.Simplified

	if comp.DeletionTimestamp != nil {
		if comp.Status.DependencyStatus != nil && comp.Status.DependencyStatus.Blocked {
			status.Status = comp.Status.DependencyStatus.Reason
			return status
		}
		status.Status = "Deleting"
		return status
	}
	if synth == nil {
		status.Status = "MissingSynthesizer"
		return status
	}

	if syn := comp.Status.InFlightSynthesis; syn != nil {
		for _, result := range syn.Results {
			if result.Severity == krmv1.ResultSeverityError {
				status.Error = result.Message
				break
			}
		}

		if syn.Canceled != nil {
			if status.Error == "" {
				status.Error = "Timeout"
			}
			status.Status = "SynthesisBackoff"
			return status
		}

		status.Status = "Synthesizing"
		return status
	}

	if !inputs.Exist(synth, comp) {
		status.Status = MissingInputStatus
		return status
	}
	if inputs.OutOfLockstep(synth, comp, comp.Status.InputRevisions) {
		status.Status = MismatchedInputsStatus
	}

	if comp.Status.CurrentSynthesis == nil && comp.Status.InFlightSynthesis == nil {
		// This means that dependencies exists and no synthesis started showing WaitingOnDependencies
		if len(comp.Spec.DependsOn) > 0 {
			status.Status = apiv1.WaitingOnDependenciesReason
			return status
		}
		status.Status = "PendingSynthesis"
		return status
	}
	if syn := comp.Status.CurrentSynthesis; syn.Ready != nil {
		status.Status = "Ready"
		return status
	}
	if syn := comp.Status.CurrentSynthesis; syn.Reconciled != nil {
		status.Status = NotReadyStatus
		return status
	}
	if syn := comp.Status.CurrentSynthesis; syn != nil && syn.Reconciled == nil {
		status.Status = ReconcilingStatus
		if current != nil {
			// Preserve any reconciliation error written by the resource slice controller
			status.Error = current.Error
		}
		return status
	}

	status.Status = "Unknown"
	return status
}

func (c *compositionController) hasActiveDependents(ctx context.Context, comp *apiv1.Composition) (bool, []apiv1.BlockedByRef, error) {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("Checking dependents status", "CompositionName", comp.GetName(), "Compositionnamespace", comp.GetNamespace())

	key := path.Join(comp.GetNamespace(), comp.GetName())
	var dependents apiv1.CompositionList
	err := c.client.List(ctx, &dependents, client.MatchingFields{manager.IdxCompositionsByDependency: key})
	if err != nil {
		logger.Error(err, "failed to list active dependents for composition")
		return false, nil, fmt.Errorf("listing dependents: %w", err)
	}

	var blockedBy []apiv1.BlockedByRef
	for _, dep := range dependents.Items {
		// Skip dependents that does not have a finalizer
		if dep.DeletionTimestamp != nil && !controllerutil.ContainsFinalizer(&dep, EnoCleanupFinalizer) {
			continue
		}

		blockedBy = append(blockedBy, apiv1.BlockedByRef{
			Name:      dep.Name,
			Namespace: dep.Namespace,
			Reason:    apiv1.WaitingOnDependentsDeletedReason,
		})
	}

	logger.Info("composition dependents check complete", "key", key, "namespace", comp.GetNamespace(), "name", comp.GetName(), "blockedBy", blockedBy)
	return len(blockedBy) > 0, blockedBy, nil
}

func (c *compositionController) updateDependencyStatus(ctx context.Context, comp *apiv1.Composition, reason string, blockedBy []apiv1.BlockedByRef) (ctrl.Result, error) {
	newStatus := &apiv1.DependencyStatus{
		Blocked:   true,
		Reason:    reason,
		BlockedBy: blockedBy,
	}
	if equality.Semantic.DeepEqual(comp.Status.DependencyStatus, newStatus) {
		return ctrl.Result{}, nil // The dependency is already up to date, no need to do anything
	}

	copy := comp.DeepCopy()
	copy.Status.DependencyStatus = newStatus
	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(comp)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating dependency status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (c *compositionController) clearDependencyStatus(ctx context.Context, comp *apiv1.Composition) (ctrl.Result, error) {
	copy := comp.DeepCopy()
	copy.Status.DependencyStatus = nil
	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(comp)); err != nil {
		return ctrl.Result{}, fmt.Errorf("clearing dependency failed: %w", err)
	}
	return ctrl.Result{}, nil
}

func (c *compositionController) terminalInFlightPod(ctx context.Context, comp *apiv1.Composition) (*corev1.Pod, bool, error) {
	syn := comp.Status.InFlightSynthesis
	if syn == nil || syn.UUID == "" {
		return nil, false, nil
	}

	var pods corev1.PodList
	err := c.client.List(ctx, &pods,
		client.InNamespace(c.synthesisPodNamespace),
		client.MatchingLabels{manager.SynthesisIDLabelKey: syn.UUID})
	if err != nil {
		return nil, false, fmt.Errorf("error listing synthesizer pods: %w", err)
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			return pod, true, nil
		}
	}
	return nil, false, nil
}

// podTerminationTime returns the time the pod actually finished running: the
// latest container termination timestamp. This is the correct anchor for the
// completion grace period ("how long after the pod finished do we wait"). It
// falls back to CreationTimestamp when no terminated state is recorded, which
// shouldn't happen for a pod already observed in a terminal phase.
func podTerminationTime(pod *corev1.Pod) time.Time {
	var latest time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		if term := cs.State.Terminated; term != nil && term.FinishedAt.Time.After(latest) {
			latest = term.FinishedAt.Time
		}
	}
	if latest.IsZero() {
		return pod.CreationTimestamp.Time
	}
	return latest
}
