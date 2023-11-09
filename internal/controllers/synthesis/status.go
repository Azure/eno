package synthesis

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// TODO: Use finalizer to avoid weird state if outside deletion?

type statusController struct {
	config *Config
	client client.Client
}

func (c *statusController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("podName", req.Name)

	pod := &corev1.Pod{}
	err := c.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting pod: %w", err))
	}
	if len(pod.OwnerReferences) == 0 || pod.OwnerReferences[0].Kind != "Composition" {
		logger.V(1).Info("skipping pod because it isn't owned by a composition")
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
	logger = logger.WithValues("composition", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizer", syn.Name, "synthesizerGeneration", syn.Generation)

	// TODO: Need to read these values from the pod

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

	return ctrl.Result{}, nil
}
