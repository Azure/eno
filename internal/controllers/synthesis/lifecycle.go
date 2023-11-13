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
	WrapperImage    string
	JobSA           string
	MaxRestarts     int32
	Timeout         time.Duration
	RolloutCooldown time.Duration
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
		Watches(&apiv1.Synthesizer{}, &synthEventHandler{ctrl: c}).
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

	// No need to create a pod if the composition's status reflects a successful synthesis and it represents the current version of the composition and synthesizer
	compInSync := comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedGeneration == comp.Generation
	synthInSync := comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
	if compInSync && synthInSync {
		logger.V(1).Info("synthesis is in sync - skipping creation")
		return ctrl.Result{}, nil
	}

	// Slow-roll synthesizer changes across referencing compositions only when the composition itself hasn't changed
	if compInSync && !synthInSync {
		res, err := c.shouldDeferRollingUpdate(ctx, comp, syn)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res != nil {
			logger.Info("deferring synthesis because another composition is within the cooldown window", "latency", res.RequeueAfter.Milliseconds())
			return *res, nil
		}
		logger.Info("re-synthesizing composition because the synthesizer configuration changed")
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

func (c *podLifecycleController) shouldDeferRollingUpdate(ctx context.Context, comp *apiv1.Composition, syn *apiv1.Synthesizer) (*ctrl.Result, error) {
	// TODO: Wait for the composition status update resulting from our last pod creation
	// - Update composition status first, then create pod
	// - Somehow defer this check until after the next status has been written
	//   - Always re-enqueue for every composition that uses the synth and isn't on its latest gen (with index)
	time.Sleep(time.Millisecond * 25)

	list := &apiv1.CompositionList{}
	if err := c.client.List(ctx, list, client.MatchingFields{
		manager.IdxCompositionsBySynthesizer: syn.Name,
	}); err != nil {
		return nil, err
	}

	for _, item := range list.Items {
		if item.Name == comp.Name && item.Namespace == comp.Namespace {
			continue // don't count the composition being reconciled
		}
		if item.Status.CurrentState == nil || item.Status.CurrentState.ObservedSynthesizerGeneration != syn.Generation {
			continue // not a relevant composition
		}

		sinceLastGeneration := time.Since(item.Status.CurrentState.PodCreation.Time)
		remainingCooldown := c.config.RolloutCooldown - sinceLastGeneration
		if remainingCooldown > 0 {
			return &ctrl.Result{Requeue: true, RequeueAfter: remainingCooldown}, nil
		}
	}

	return nil, nil
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

		if isCurrent && comp.Status.CurrentState != nil {
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
