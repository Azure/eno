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
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)

	// Delete any unnecessary pods
	pods := &corev1.PodList{}
	err = c.client.List(ctx, pods, client.MatchingFields{
		manager.IdxPodsByComposition: comp.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}
	logger, toDelete, exists := c.shouldDeletePod(logger, comp, syn, pods)
	if toDelete != nil {
		if err := c.client.Delete(ctx, toDelete); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("deleting pod: %w", err))
		}
		logger.Info("deleted synthesizer pod", "podName", toDelete.Name)
		return ctrl.Result{}, nil
	}
	if exists {
		// The pod is still running.
		// Poll periodically to check if has timed out.
		return ctrl.Result{RequeueAfter: c.config.Timeout}, nil
	}

	// Swap the state to prepare for resynthesis if needed
	if comp.Status.CurrentState == nil || comp.Status.CurrentState.ObservedCompositionGeneration != comp.Generation {
		swapStates(syn, comp)
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}
		logger.Info("start to synthesize")
		return ctrl.Result{}, nil
	}

	// No need to create a pod if everything is in sync
	if comp.Status.CurrentState != nil && comp.Status.CurrentState.Synthesized {
		return ctrl.Result{}, nil
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, c.client.Scheme(), comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating pod: %w", err))
	}
	logger.Info("created synthesizer pod", "podName", pod.Name)

	return ctrl.Result{}, nil
}

func (c *podLifecycleController) shouldDeletePod(logger logr.Logger, comp *apiv1.Composition, syn *apiv1.Synthesizer, pods *corev1.PodList) (logr.Logger, *corev1.Pod /* exists */, bool) {
	if len(pods.Items) == 0 {
		return logger, nil, false
	}

	// Only create pods when the previous one is deleting or non-existant
	for _, pod := range pods.Items {
		pod := pod
		if pod.DeletionTimestamp != nil {
			continue // already deleted
		}

		isCurrent := podDerivedFrom(comp, &pod)
		if !isCurrent {
			logger = logger.WithValues("reason", "Superseded")
			return logger, &pod, true
		}

		// TODO: Is it necessary to allow concurrent pods while one is terminating to avoid deadlocks?

		// Synthesis is done
		if comp.Status.CurrentState != nil && comp.Status.CurrentState.Synthesized {
			if comp.Status.CurrentState != nil && comp.Status.CurrentState.PodCreation != nil {
				logger = logger.WithValues("latency", time.Since(comp.Status.CurrentState.PodCreation.Time).Milliseconds())
			}
			logger = logger.WithValues("reason", "Success")
			return logger, &pod, true
		}

		// Pod is too old
		// We timeout eventually in case it landed on a node that for whatever reason isn't capable of running the pod
		if time.Since(pod.CreationTimestamp.Time) > c.config.Timeout {
			if comp.Status.CurrentState != nil && comp.Status.CurrentState.PodCreation != nil {
				logger = logger.WithValues("latency", time.Since(comp.Status.CurrentState.PodCreation.Time).Milliseconds())
			}
			logger = logger.WithValues("reason", "Timeout")
			return logger, &pod, true
		}

		// At this point the pod should still be running - no need to check other pods
		return logger, nil, true
	}
	return logger, nil, false
}

func swapStates(syn *apiv1.Synthesizer, comp *apiv1.Composition) {
	// TODO: Is there ever a case where we would _not_ want to swap current->prev? Such as if prev succeeded but cur hasn't?
	comp.Status.PreviousState = comp.Status.CurrentState
	comp.Status.CurrentState = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
	}
}
