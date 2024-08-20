package synthesis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type Config struct {
	SliceCreationQPS  float64
	ExecutorImage     string
	PodNamespace      string
	PodServiceAccount string

	TaintTolerationKey   string
	TaintTolerationValue string

	NodeAffinityKey   string
	NodeAffinityValue string
}

type podLifecycleController struct {
	config        *Config
	client        client.Client
	noCacheReader client.Reader
}

// NewPodLifecycleController is responsible for creating and deleting pods as needed to synthesize compositions.
func NewPodLifecycleController(mgr ctrl.Manager, cfg *Config) error {
	c := &podLifecycleController{
		config:        cfg,
		client:        mgr.GetClient(),
		noCacheReader: mgr.GetAPIReader(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(manager.PodToCompMapFunc)).
		WithLogConstructor(manager.NewLogConstructor(mgr, "podLifecycleController")).
		Complete(c)
}

func (c *podLifecycleController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition resource: %w", err))
	}
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

	// It isn't safe to delete compositions until their resource slices have been cleaned up,
	// since reconciling resources necessarily requires the composition.
	if comp.DeletionTimestamp == nil && controllerutil.AddFinalizer(comp, "eno.azure.io/cleanup") {
		err = c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition: %w", err)
		}
		logger.V(1).Info("added cleanup finalizer to composition")
		return ctrl.Result{}, nil
	}

	// Delete any unnecessary pods
	pods := &corev1.PodList{}
	err = c.client.List(ctx, pods, client.InNamespace(c.config.PodNamespace), client.MatchingFields{
		manager.IdxPodsByComposition: manager.PodByCompIdxValueFromComp(comp),
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}

	// Tolerate missing synths since we may still need to cleanup
	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	// It's only safe to ignore as a missing synth if we have already started synthesis,
	// otherwise creating the synth and composition around the same time could result in a deadlock
	// if the composition is processed before the synth hits the informer cache.
	if (errors.IsNotFound(err) || syn.DeletionTimestamp != nil) && comp.Status.CurrentSynthesis != nil {
		syn = nil
		err = nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	if syn != nil {
		logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)
	}

	logger, toDelete, exists := shouldDeletePod(logger, comp, syn, pods)
	if toDelete != nil {
		if err := c.client.Delete(ctx, toDelete); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("deleting pod: %w", err))
		}
		logger.V(0).Info("deleted synthesizer pod", "podName", toDelete.Name)
		return ctrl.Result{}, nil
	}
	if comp.DeletionTimestamp != nil {
		ctx = logr.NewContext(ctx, logger)
		return c.reconcileDeletedComposition(ctx, comp)
	}
	if exists {
		// The pod is still running.
		// Poll periodically to check if has timed out.
		if syn.Spec.PodTimeout == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: syn.Spec.PodTimeout.Duration}, nil
	}

	// Synthesis isn't possible without a synth
	if syn == nil {
		return ctrl.Result{}, nil
	}

	// Swap the state to prepare for resynthesis if needed
	if shouldSwapStates(syn, comp) {
		SwapStates(comp)
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}
		logger.V(0).Info("start to synthesize")
		return ctrl.Result{Requeue: true}, nil
	}

	// Bail if it isn't time to synthesize yet, or synthesis is already complete
	if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.UUID == "" || comp.Status.CurrentSynthesis.Synthesized != nil || comp.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	// Back off to avoid constantly re-synthesizing impossible compositions (unlikely but possible)
	if shouldBackOffPodCreation(comp) {
		const base = time.Millisecond * 250
		wait := base * time.Duration(comp.Status.CurrentSynthesis.Attempts)
		nextAttempt := comp.Status.CurrentSynthesis.PodCreation.Time.Add(wait)
		if time.Since(nextAttempt) < 0 { // positive when past the nextAttempt
			logger.V(1).Info("backing off pod creation", "latency", wait.Milliseconds())
			return ctrl.Result{RequeueAfter: wait}, nil
		}
	}

	// Confirm that a pod doesn't already exist for this synthesis without trusting informers.
	// This protects against cases where synthesis has recently started and something causes
	// another tick of this loop before the pod write hits the informer.
	err = c.noCacheReader.List(ctx, pods, client.MatchingLabels{
		"eno.azure.io/synthesis-uuid": comp.Status.CurrentSynthesis.UUID,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking for existing pod: %w", err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			logger.V(1).Info(fmt.Sprintf("refusing to create new synthesizer pod because the pod %q already exists and has not been deleted", pod.Name))
			return ctrl.Result{}, nil
		}
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating pod: %w", err)
	}
	logger.V(0).Info("created synthesizer pod", "podName", pod.Name)
	sytheses.Inc()

	// This metadata is optional - it's safe for the process to crash before reaching this point
	patch := []map[string]any{
		{"op": "test", "path": "/status/currentSynthesis/uuid", "value": comp.Status.CurrentSynthesis.UUID},
		{"op": "test", "path": "/status/currentSynthesis/synthesized", "value": nil},
		{"op": "replace", "path": "/status/currentSynthesis/attempts", "value": comp.Status.CurrentSynthesis.Attempts + 1},
		{"op": "replace", "path": "/status/currentSynthesis/podCreation", "value": pod.CreationTimestamp},
	}
	patchJS, err := json.Marshal(&patch)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("encoding patch: %w", err)
	}

	if err := c.client.Status().Patch(ctx, comp, client.RawPatch(types.JSONPatchType, patchJS)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status after synthesizer pod creation: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *podLifecycleController) reconcileDeletedComposition(ctx context.Context, comp *apiv1.Composition) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// If the composition was being synthesized at the time of deletion we need to swap the previous
	// state back to current. Otherwise we'll get stuck waiting for a synthesis that can't happen.
	if shouldRevertStateSwap(comp) {
		comp.Status.CurrentSynthesis = comp.Status.PreviousSynthesis
		comp.Status.PreviousSynthesis = nil
		err := c.client.Status().Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reverting swapped status for deletion: %w", err)
		}
		logger.V(0).Info("reverted swapped status for deletion")
		return ctrl.Result{}, nil
	}

	// Deletion increments the composition's generation, but the reconstitution cache is only invalidated
	// when the synthesized generation (from the status) changes, which will never happen because synthesis
	// is righly disabled for deleted compositions. We break out of this deadlock condition by updating
	// the status without actually synthesizing.
	if shouldUpdateDeletedCompositionStatus(comp) {
		comp.Status.CurrentSynthesis.ObservedCompositionGeneration = comp.Generation
		comp.Status.CurrentSynthesis.Ready = nil
		comp.Status.CurrentSynthesis.UUID = uuid.NewString()
		comp.Status.CurrentSynthesis.Reconciled = nil
		now := metav1.Now()
		comp.Status.CurrentSynthesis.Synthesized = &now
		err := c.client.Status().Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating current composition generation: %w", err)
		}
		logger.V(0).Info("updated composition status to reflect deletion")
		return ctrl.Result{}, nil
	}

	// Remove the finalizer when all pods and slices have been deleted
	if isReconciling(comp) {
		logger.V(1).Info("refusing to remove composition finalizer because it is still being reconciled")
		return ctrl.Result{}, nil
	}
	if controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}

		logger.V(0).Info("removed finalizer from composition")
	}

	return ctrl.Result{}, nil
}

func shouldDeletePod(logger logr.Logger, comp *apiv1.Composition, syn *apiv1.Synthesizer, pods *corev1.PodList) (logr.Logger, *corev1.Pod, bool /* exists */) {
	if len(pods.Items) == 0 {
		return logger, nil, false
	}

	// Only create pods when the previous one is deleting or non-existant
	var onePodDeleting bool
	for _, pod := range pods.Items {
		pod := pod

		// Allow a single extra pod to be created while the previous one is terminating
		// in order to break potential deadlocks while avoiding a thundering herd of pods
		if pod.DeletionTimestamp != nil {
			if onePodDeleting {
				return logger, nil, true
			}
			onePodDeleting = true
			continue
		}

		if len(pod.Status.ContainerStatuses) > 0 {
			logger = logger.WithValues("restarts", pod.Status.ContainerStatuses[0].RestartCount)
		}

		if syn == nil {
			logger = logger.WithValues("reason", "SynthesizerDeleted")
			return logger, &pod, true
		}

		if comp.DeletionTimestamp != nil {
			logger = logger.WithValues("reason", "CompositionDeleted")
			return logger, &pod, true
		}

		if pod.Status.Phase == corev1.PodSucceeded {
			logger = logger.WithValues("reason", "Complete")
			return logger, &pod, true
		}

		isCurrent := podIsCurrent(comp, &pod)
		if !isCurrent {
			logger = logger.WithValues("reason", "Superseded")
			return logger, &pod, true
		}

		// Synthesis is done
		if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil {
			if comp.Status.CurrentSynthesis.PodCreation != nil {
				logger = logger.WithValues("latency", time.Since(comp.Status.CurrentSynthesis.PodCreation.Time).Milliseconds())
			}
			logger = logger.WithValues("reason", "Success")
			return logger, &pod, true
		}

		// Pod is too old
		// We timeout eventually in case it landed on a node that for whatever reason isn't capable of running the pod
		if time.Since(pod.CreationTimestamp.Time) > syn.Spec.PodTimeout.Duration {
			logger = logger.WithValues("reason", "Timeout")
			synthesPodRecreations.Inc()
			return logger, &pod, true
		}

		// At this point the pod should still be running - no need to check other pods
		return logger, nil, true
	}
	return logger, nil, false
}

func SwapStates(comp *apiv1.Composition) {
	current := comp.Status.CurrentSynthesis
	if current != nil && current.Synthesized != nil && !current.Failed() {
		comp.Status.PreviousSynthesis = current
	}

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
	}
}

func shouldSwapStates(synth *apiv1.Synthesizer, comp *apiv1.Composition) bool {
	// synthesize when (either):
	// - synthesis has never occurred
	// - the spec has changed
	// - the bound input resources have changed
	// AND
	// - synthesis is not already pending
	// - all bound input resources exist and are in lockstep (or composition is being deleted)
	syn := comp.Status.CurrentSynthesis
	return (comp.DeletionTimestamp != nil || (comp.InputsExist(synth) && !comp.InputsMismatched(synth))) && (syn == nil || syn.ObservedCompositionGeneration != comp.Generation || (!inputRevisionsEqual(synth, comp.Status.InputRevisions, syn.InputRevisions) && syn.Synthesized != nil))
}

func shouldBackOffPodCreation(comp *apiv1.Composition) bool {
	current := comp.Status.CurrentSynthesis
	return current != nil && current.Attempts > 0 && current.PodCreation != nil
}

func shouldRevertStateSwap(comp *apiv1.Composition) bool {
	return comp.Status.PreviousSynthesis != nil && (comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil)
}

func shouldUpdateDeletedCompositionStatus(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && (comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation || comp.Status.CurrentSynthesis.Synthesized == nil)
}

func isReconciling(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis != nil && (comp.Status.CurrentSynthesis.Reconciled == nil || comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation)
}

// inputRevisionsEqual compares two sets of input revisions while ignoring deferred values.
func inputRevisionsEqual(synth *apiv1.Synthesizer, a, b []apiv1.InputRevisions) bool {
	if len(a) != len(b) {
		return false
	}

	refsByKey := map[string]apiv1.Ref{}
	for _, ref := range synth.Spec.Refs {
		ref := ref
		refsByKey[ref.Key] = ref
	}

	var equal int
	for _, ar := range a {
		for _, br := range b {
			if ref, exists := refsByKey[ar.Key]; exists && ref.Defer {
				equal++
				continue // ignore deferred inputs
			}

			if ar.Equal(br) {
				equal++
			}
		}
	}

	return equal == len(a)
}
