package synthesis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type Config struct {
	SliceCreationQPS float64
}

type podLifecycleController struct {
	config           *Config
	client           client.Client
	createSliceLimit flowcontrol.RateLimiter
}

// NewPodLifecycleController is responsible for creating and deleting pods as needed to synthesize compositions.
func NewPodLifecycleController(mgr ctrl.Manager, cfg *Config) error {
	c := &podLifecycleController{
		config:           cfg,
		client:           mgr.GetClient(),
		createSliceLimit: flowcontrol.NewTokenBucketRateLimiter(float32(cfg.SliceCreationQPS), 1),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
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
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

	// It isn't safe to delete compositions until their resource slices have been cleaned up,
	// since reconciling resources necessarily requires the composition.
	if comp.DeletionTimestamp == nil && controllerutil.AddFinalizer(comp, "eno.azure.io/cleanup") {
		err = c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition: %w", err)
		}
		logger.Info("added cleanup finalizer to composition")
		return ctrl.Result{}, nil
	}

	// Delete any unnecessary pods
	pods := &corev1.PodList{}
	err = c.client.List(ctx, pods, client.InNamespace(comp.Namespace), client.MatchingFields{
		manager.IdxPodsByComposition: comp.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting synthesizer: %w", err))
	}
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)

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
		return ctrl.Result{RequeueAfter: syn.Spec.PodTimeout.Duration}, nil
	}

	if comp.DeletionTimestamp != nil {
		sliceList := &apiv1.ResourceSliceList{}
		err = c.client.List(ctx, sliceList, client.MatchingFields{
			manager.IdxResourceSlicesByComposition: comp.Name,
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("listing resource slices: %w", err)
		}

		// Deletion increments the composition's generation, but the reconstitution cache is only invalidated
		// when the synthesized generation (from the status) changes, which will never happen because synthesis
		// is righly disabled for deleted compositions. We break out of this deadlock condition by updating
		// the status without actually synthesizing.
		if comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedCompositionGeneration != comp.Generation {
			comp.Status.CurrentState.ObservedCompositionGeneration = comp.Generation
			comp.Status.CurrentState.Synthesized = true // in case the previous synthesis failed
			err = c.client.Status().Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating current composition generation: %w", err)
			}
			logger.Info("updated composition status to reflect deletion")
			return ctrl.Result{}, nil
		}

		// Remove the finalizer when all pods and slices have been deleted
		if len(sliceList.Items) > 0 || len(pods.Items) > 0 {
			logger.V(1).Info(fmt.Sprintf("refusing to remove composition finalizer because %d associated resource slices and %d pods still exist", len(sliceList.Items), len(pods.Items)))
			return ctrl.Result{}, nil // some resources still exist
		}
		if controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup") {
			err = c.client.Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}

			logger.Info("removed finalizer from composition because none of its resource slices or synthesizer pods remain")
		}

		return ctrl.Result{}, nil
	}

	// Swap the state to prepare for resynthesis if needed
	if comp.Status.CurrentState == nil || comp.Status.CurrentState.ObservedCompositionGeneration != comp.Generation {
		swapStates(comp)
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}
		logger.Info("start to synthesize")
		return ctrl.Result{}, nil
	}

	// No need to create a pod if everything is in sync
	if comp.Status.CurrentState != nil && comp.Status.CurrentState.Synthesized || comp.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, c.client.Scheme(), comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating pod: %w", err))
	}
	logger.Info("created synthesizer pod", "podName", pod.Name)
	sytheses.Inc()

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

		if comp.DeletionTimestamp != nil {
			logger = logger.WithValues("reason", "CompositionDeleted")
			return logger, &pod, true
		}

		isCurrent := podDerivedFrom(comp, &pod)
		if !isCurrent {
			logger = logger.WithValues("reason", "Superseded")
			return logger, &pod, true
		}

		// TODO: Allow a second concurrent pod only while one is terminating (to avoid deadlocks)

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
		if time.Since(pod.CreationTimestamp.Time) > syn.Spec.PodTimeout.Duration {
			if comp.Status.CurrentState != nil && comp.Status.CurrentState.PodCreation != nil {
				logger = logger.WithValues("latency", time.Since(comp.Status.CurrentState.PodCreation.Time).Milliseconds())
			}
			logger = logger.WithValues("reason", "Timeout")
			synthesPodRecreations.Inc()
			return logger, &pod, true
		}

		// At this point the pod should still be running - no need to check other pods
		return logger, nil, true
	}
	return logger, nil, false
}

func swapStates(comp *apiv1.Composition) {
	// If the previous state has been synthesized but not the current, keep the previous to avoid orphaning deleted resources
	if comp.Status.CurrentState != nil && comp.Status.CurrentState.Synthesized {
		comp.Status.PreviousState = comp.Status.CurrentState
	}
	comp.Status.CurrentState = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
	}
}
