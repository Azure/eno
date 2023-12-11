package synthesis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type Config struct {
	WrapperImage string
	JobSA        string
	MaxRestarts  int32
	Timeout      time.Duration
}

type podLifecycleController struct {
	config *Config
	client client.Client
}

// NewPodLifecycleController is responsible for creating and deleting pods as needed to synthesize compositions.
func NewPodLifecycleController(mgr ctrl.Manager, cfg *Config) error {
	c := &podLifecycleController{
		config: cfg,
		client: mgr.GetClient(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&corev1.Pod{}).
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
	if comp.Spec.Synthesizer.Name == "" {
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("composition", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizer", syn.Name, "synthesizerGeneration", syn.Generation)

	// Delete any unnecessary pods
	pods := &corev1.PodList{}
	err = c.client.List(ctx, pods, client.MatchingFields{
		manager.IdxPodsByComposition: comp.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}
	logger, toDelete, ok := c.shouldDeletePod(logger, comp, syn, pods)
	if !ok && toDelete == nil {
		// The pod is still running.
		// Poll periodically to check if has timed out.
		return ctrl.Result{RequeueAfter: c.config.Timeout}, nil
	}
	if !ok && toDelete != nil {
		if err := c.client.Delete(ctx, toDelete); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting pod: %w", err)
		}
		logger.Info("deleted pod", "podName", toDelete.Name)
		return ctrl.Result{}, nil
	}

	// Swap the state to prepare for resynthesis if needed
	if comp.Status.CurrentState == nil || comp.Status.CurrentState.ObservedCompositionGeneration != comp.Generation {
		swapStates(syn, comp)
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}
		logger.Info("swapped composition state because composition was modified since last synthesis")
		return ctrl.Result{}, nil
	}

	// No need to create a pod if everything is in sync
	if comp.Status.CurrentState != nil && comp.Status.CurrentState.ResourceSliceCount != nil {
		return ctrl.Result{}, nil
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, c.client.Scheme(), comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating pod: %w", err))
	}
	logger.Info("created pod", "podName", pod.Name)

	return ctrl.Result{}, nil
}

func (c *podLifecycleController) shouldDeletePod(logger logr.Logger, comp *apiv1.Composition, syn *apiv1.Synthesizer, pods *corev1.PodList) (logr.Logger, *corev1.Pod, bool) {
	if len(pods.Items) == 0 {
		return logger, nil, true
	}

	// Just in case we somehow created more than one pod (stale informer cache, etc.) - delete duplicates
	var activeLatest bool
	for _, pod := range pods.Items {
		pod := pod
		if pod.DeletionTimestamp != nil || !podDerivedFrom(comp, syn, &pod) {
			continue
		}
		if activeLatest {
			logger = logger.WithValues("reason", "Duplicate")
			return logger, &pod, false
		}
		activeLatest = true
	}

	// Only create pods when the previous one is deleting or non-existant
	for _, pod := range pods.Items {
		pod := pod
		reason, shouldDelete := c.podStatusTerminal(&pod)
		isCurrent := podDerivedFrom(comp, syn, &pod)

		// If the current pod is being deleted it's safe to create a new one if needed
		// Avoid getting stuck by pods that fail to delete
		if pod.DeletionTimestamp != nil && isCurrent {
			return logger, nil, true
		}

		// Pod exists but still has work to do
		if isCurrent && !shouldDelete {
			continue
		}

		// Don't delete pods again
		if pod.DeletionTimestamp != nil {
			continue // already deleted
		}

		if isCurrent && comp.Status.CurrentState != nil && comp.Status.CurrentState.PodCreation != nil {
			logger = logger.WithValues("latency", time.Since(comp.Status.CurrentState.PodCreation.Time).Milliseconds())
		}
		if shouldDelete {
			logger = logger.WithValues("reason", reason)
		}
		if !isCurrent {
			logger = logger.WithValues("reason", "Superseded")
		}
		return logger, &pod, false
	}
	return logger, nil, false
}

func (c *podLifecycleController) podStatusTerminal(pod *corev1.Pod) (string, bool) {
	if time.Since(pod.CreationTimestamp.Time) > c.config.Timeout {
		return "Timeout", true
	}
	for _, cont := range append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...) {
		if cont.RestartCount > c.config.MaxRestarts {
			return "MaxRestartsExceeded", true
		}
		if cont.State.Terminated != nil && cont.State.Terminated.ExitCode == 0 {
			return "Succeeded", true
		}
		if cont.State.Terminated == nil || cont.State.Terminated.ExitCode != 0 {
			return "", false // has not completed yet
		}
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		return "Unknown", true // shouldn't be possible
	}
	return "", false // status not initialized yet
}

func swapStates(syn *apiv1.Synthesizer, comp *apiv1.Composition) {
	// TODO: Block swapping prev->current if the any resources present in prev but absent in current have not yet been reconciled
	// This will ensure that we don't orphan resources
	comp.Status.PreviousState = comp.Status.CurrentState
	comp.Status.CurrentState = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
	}
}
