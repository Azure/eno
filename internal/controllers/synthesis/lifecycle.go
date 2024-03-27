package synthesis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type Config struct {
	SliceCreationQPS  float64
	PodNamespace      string
	PodServiceAccount string
}

type podLifecycleController struct {
	config           *Config
	client           client.Client
	noCacheReader    client.Reader
	createSliceLimit flowcontrol.RateLimiter
}

// NewPodLifecycleController is responsible for creating and deleting pods as needed to synthesize compositions.
func NewPodLifecycleController(mgr ctrl.Manager, cfg *Config) error {
	c := &podLifecycleController{
		config:           cfg,
		client:           mgr.GetClient(),
		noCacheReader:    mgr.GetAPIReader(),
		createSliceLimit: flowcontrol.NewTokenBucketRateLimiter(float32(cfg.SliceCreationQPS), 1),
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
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)

	logger, toDelete, exists := shouldDeletePod(logger, comp, syn, pods)
	if toDelete != nil {
		if err := c.client.Delete(ctx, toDelete); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("deleting pod: %w", err))
		}
		logger.V(0).Info("deleted synthesizer pod", "podName", toDelete.Name)
		return ctrl.Result{}, nil
	}
	if exists {
		// The pod is still running.
		// Poll periodically to check if has timed out.
		if syn.Spec.PodTimeout == nil {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: syn.Spec.PodTimeout.Duration}, nil
	}

	if comp.DeletionTimestamp != nil {
		// If the composition was being synthesized at the time of deletion we need to swap the previous
		// state back to current. Otherwise we'll get stuck waiting for a synthesis that can't happen.
		if comp.Status.PreviousSynthesis != nil && (comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil) {
			comp.Status.CurrentSynthesis = comp.Status.PreviousSynthesis
			comp.Status.PreviousSynthesis = nil
			err = c.client.Status().Update(ctx, comp)
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
		if comp.Status.CurrentSynthesis != nil && (comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation || comp.Status.CurrentSynthesis.Synthesized == nil) {
			comp.Status.CurrentSynthesis.ObservedCompositionGeneration = comp.Generation
			comp.Status.CurrentSynthesis.Ready = nil
			comp.Status.CurrentSynthesis.Reconciled = nil
			now := metav1.Now()
			comp.Status.CurrentSynthesis.Synthesized = &now
			err = c.client.Status().Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating current composition generation: %w", err)
			}
			logger.V(0).Info("updated composition status to reflect deletion")
			return ctrl.Result{}, nil
		}

		// Remove the finalizer when all pods and slices have been deleted
		if comp.Status.CurrentSynthesis != nil && (comp.Status.CurrentSynthesis.Reconciled == nil) || comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation {
			logger.V(1).Info("refusing to remove composition finalizer because it is still being reconciled")
			return ctrl.Result{}, nil
		}
		if hasRunningPod(pods) {
			logger.V(1).Info("refusing to remove composition finalizer because at least one synthesizer pod still exists")
			return ctrl.Result{}, nil
		}
		if controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup") {
			err = c.client.Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}

			logger.V(0).Info("removed finalizer from composition")
		}

		return ctrl.Result{}, nil
	}

	// Swap the state to prepare for resynthesis if needed
	if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.ObservedCompositionGeneration != comp.Generation {
		swapStates(comp, syn)
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}
		logger.V(0).Info("start to synthesize")
		return ctrl.Result{}, nil
	}

	// No need to create a pod if everything is in sync
	if comp.Status.CurrentSynthesis.Synthesized != nil || comp.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	// Don't attempt to synthesize a composition that doesn't reference a synthesizer.
	if comp.Spec.Synthesizer.Name == "" {
		return ctrl.Result{}, nil
	}

	// Before it's safe to create a pod, we need to write _something_ back to the composition.
	// This will fail if another write has already hit the resource i.e. synthesis completion.
	// Otherwise it's possible for pod deletion event to land before the synthesis complete event.
	// In that case a new pod would be created even though the synthesis just completed.
	if comp.Status.CurrentSynthesis.UUID == "" {
		comp.Status.CurrentSynthesis.UUID = uuid.NewString()
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("writing started timestamp to status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Back off to avoid constantly re-synthesizing impossible compositions (unlikely but possible)
	if current := comp.Status.CurrentSynthesis; current != nil && current.Attempts > 0 && current.PodCreation != nil {
		const base = time.Millisecond * 250
		wait := base * time.Duration(comp.Status.CurrentSynthesis.Attempts)
		nextAttempt := current.PodCreation.Time.Add(wait)
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
			logger.V(0).Info(fmt.Sprintf("refusing to create new synthesizer pod because the pod %q already exists and has not been deleted", pod.Name))
			return ctrl.Result{}, nil
		}
	}

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, c.client.Scheme(), comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating pod: %w", err)
	}
	logger.V(0).Info("created synthesizer pod", "podName", pod.Name)
	sytheses.Inc()

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
		if comp.DeletionTimestamp != nil {
			logger = logger.WithValues("reason", "CompositionDeleted")
			return logger, &pod, true
		}

		// Allow a single extra pod to be created while the previous one is terminating
		// in order to break potential deadlocks while avoiding a thundering herd of pods
		// TODO: e2e test for this
		if pod.DeletionTimestamp != nil {
			if onePodDeleting {
				return logger, nil, true
			}
			onePodDeleting = true
			continue
		}

		isCurrent := podDerivedFrom(comp, &pod)
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

func swapStates(comp *apiv1.Composition, syn *apiv1.Synthesizer) {
	// Reset the current attempts counter when the composition or synthesizer have changed or synthesis was successful
	attempts := 0
	if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized == nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == comp.Generation && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation {
		attempts = comp.Status.CurrentSynthesis.Attempts
	}

	// If the previous state has been synthesized but not the current, keep the previous to avoid orphaning deleted resources
	if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil {
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
	}

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		ObservedCompositionGeneration: comp.Generation,
		Attempts:                      attempts,
	}
}

func hasRunningPod(list *corev1.PodList) bool {
	for _, pod := range list.Items {
		if pod.DeletionTimestamp == nil {
			return true
		}
	}
	return false
}
