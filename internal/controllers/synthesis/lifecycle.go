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
)

type lifecycleController struct {
	config *Config
	client client.Client
	logger logr.Logger
}

func (c *lifecycleController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	err := c.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting pod: %w", err))
	}
	if len(pod.OwnerReferences) == 0 || pod.OwnerReferences[0].Kind != "Composition" {
		// TODO: Log
		return ctrl.Result{}, nil
	}

	comp := &apiv1.Composition{}
	comp.Name = pod.OwnerReferences[0].Name
	comp.Namespace = pod.Namespace
	err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition resource: %w", err))
	}
	if comp.Spec.Synthesizer.Name == "" {
		return ctrl.Result{}, nil
	}

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}

	logger := c.logger.WithValues("composition", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesizer", syn.Name, "synthesizerGeneration", syn.Generation)

	// Populate the status of the active synthesis
	if comp.Status.CurrentState == nil || comp.Status.CurrentState.ObservedGeneration != comp.Generation || comp.Status.CurrentState.ObservedSynthesizerGeneration != syn.Generation {
		if comp.Status.CurrentState != nil && comp.Status.CurrentState.ResourceSliceCount != nil {
			// Only swap current->previous when the current synthesis has completed
			// This avoids losing the prior state during rapid updates to the composition
			comp.Status.PreviousState = comp.Status.CurrentState
		}
		comp.Status.CurrentState = &apiv1.Synthesis{
			ObservedGeneration:            comp.Generation,
			ObservedSynthesizerGeneration: syn.Generation,
			PodCreation:                   pod.CreationTimestamp,
		}
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
		}
		logger.V(1).Info("added this synthesis to the composition status")
		return ctrl.Result{}, nil
	}

	// Clean up if the pod is no longer needed
	if pod.DeletionTimestamp == nil && c.shouldDeletePod(pod) {
		err = c.client.Delete(ctx, pod)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("error deleting pod: %w", err)
		}
		logger.Info("deleted synthesis pod")
	}

	// At this point we know the pod is still running.
	// Poll periodically to check if has timed out.
	return ctrl.Result{RequeueAfter: c.config.Timeout}, nil
}

func (c *lifecycleController) shouldDeletePod(pod *corev1.Pod) bool {
	if time.Since(pod.CreationTimestamp.Time) > c.config.Timeout {
		return true
	}
	for _, cont := range append(pod.Status.ContainerStatuses, pod.Status.InitContainerStatuses...) {
		if cont.RestartCount > c.config.MaxRestarts {
			return true
		}

		if cont.State.Terminated == nil || cont.State.Terminated.ExitCode != 0 {
			return false // has not completed yet
		}
	}
	return len(pod.Status.ContainerStatuses) > 0
}
