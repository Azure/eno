package synthesis

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type podGarbageCollector struct {
	client          client.Client
	creationTimeout time.Duration
}

func NewPodGC(mgr ctrl.Manager, creationTimeout time.Duration) error {
	c := &podGarbageCollector{
		client:          mgr.GetClient(),
		creationTimeout: creationTimeout,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "podGarbageCollector")).
		Complete(c)
}

func (p *podGarbageCollector) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	pod := &corev1.Pod{}
	err := p.client.Get(ctx, req.NamespacedName, pod)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get pod")
		return ctrl.Result{}, err
	}
	if pod.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("podName", pod.Name, "podNamespace", pod.Namespace)

	if pod.Labels == nil {
		logger.V(0).Info("saw a pod without any labels - this shouldn't be possible!")
		return ctrl.Result{}, nil
	}

	// Avoid waiting for the lease to expire for broken nodes
	if delta := timeWaitingForKubelet(pod, time.Now()); delta > 0 {
		if delta < p.creationTimeout {
			return ctrl.Result{RequeueAfter: p.creationTimeout - delta}, nil
		}
		logger = logger.WithValues("reason", "ContainerCreationTimeout")
		return ctrl.Result{}, p.deletePod(ctx, pod, logger)
	}

	// GC pods from deleted compositions
	comp := &apiv1.Composition{}
	comp.Name = pod.GetLabels()[compositionNameLabelKey]
	comp.Namespace = pod.GetLabels()[compositionNamespaceLabelKey]
	err = p.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisAge", synthesisAge(comp))
	if errors.IsNotFound(err) || comp.DeletionTimestamp != nil {
		logger = logger.WithValues("reason", "CompositionDeleted")
		return ctrl.Result{}, p.deletePod(ctx, pod, logger)
	}
	if err != nil {
		logger.Error(err, "failed to get composition resource")
		return ctrl.Result{}, err
	}

	// GC pods from missing synthesizers
	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = p.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerGeneration", syn.Generation)
	if errors.IsNotFound(err) {
		logger = logger.WithValues("reason", "SynthesizerDeleted")
		return ctrl.Result{}, p.deletePod(ctx, pod, logger)
	}
	if err != nil {
		logger.Error(err, "failed to get synthesizer")
		return ctrl.Result{}, err
	}

	// Ignore brand new pods since the pod/composition informer might not be in sync
	const gracePeriod = time.Second
	delta := gracePeriod - time.Since(pod.CreationTimestamp.Time)
	if delta > 0 {
		return ctrl.Result{RequeueAfter: delta}, nil
	}

	// The image tag must match the current synthesizer, otherwise other properties (e.g. refs) may be incorrect
	if img := findContainerImage(pod); img != "" && img != syn.Spec.Image {
		logger = logger.WithValues("reason", "ImageChanged")
		return ctrl.Result{}, p.deletePod(ctx, pod, logger)
	}

	if syn := comp.Status.InFlightSynthesis; syn != nil {
		if syn.Canceled != nil {
			logger = logger.WithValues("reason", "Timeout")
			return ctrl.Result{}, p.deletePod(ctx, pod, logger)
		}

		// A new synthesis has replaced the previous
		if syn.UUID != pod.Labels[synthesisIDLabelKey] {
			logger = logger.WithValues("reason", "Superseded")
			return ctrl.Result{}, p.deletePod(ctx, pod, logger)
		}

		return ctrl.Result{RequeueAfter: time.Second}, nil // still active
	}

	// In-flight synthesis being swapped to current == synthesis completed
	if syn := comp.Status.CurrentSynthesis; syn != nil && syn.UUID == pod.Labels[synthesisIDLabelKey] {
		logger = logger.WithValues("reason", "Success")
		return ctrl.Result{}, p.deletePod(ctx, pod, logger)
	}

	// This condition should only be able to happen when the composition has been deleted
	logger = logger.WithValues("reason", "Orphaned")
	return ctrl.Result{}, p.deletePod(ctx, pod, logger)
}

func (p *podGarbageCollector) deletePod(ctx context.Context, pod *corev1.Pod, logger logr.Logger) error {
	if len(pod.Status.ContainerStatuses) > 0 {
		logger = logger.WithValues("restarts", pod.Status.ContainerStatuses[0].RestartCount)
	}

	// Check for image pull errors before deleting the pod
	if errMsg, hasError := checkImagePullError(pod); hasError {
		logger.Info("detected image pull error in synthesizer pod", "errorMessage", errMsg)

		// Try to update the composition with the error message
		if err := p.updateCompositionWithImagePullError(ctx, pod, errMsg, logger); err != nil {
			logger.Error(err, "failed to update composition with image pull error")
			// Continue with pod deletion even if the composition update fails
		}
	}

	err := p.client.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}})
	if err != nil {
		return fmt.Errorf("deleting pod: %w", err)
	}
	logger.Info("deleted synthesizer pod", "latency", time.Since(pod.CreationTimestamp.Time).Milliseconds())
	return nil
}

func timeWaitingForKubelet(pod *corev1.Pod, now time.Time) time.Duration {
	if len(pod.Status.ContainerStatuses) > 0 {
		return 0
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type != corev1.PodScheduled {
			continue
		}
		if cond.Status == corev1.ConditionFalse {
			return 0
		}
		scheduledTime := &cond.LastTransitionTime.Time
		return now.Sub(*scheduledTime)
	}
	return 0
}

func synthesisAge(comp *apiv1.Composition) *int64 {
	syn := comp.Status.InFlightSynthesis
	if syn == nil || syn.Initialized == nil {
		return nil
	}
	return ptr.To(time.Since(syn.Initialized.Time).Milliseconds())
}

// checkImagePullError checks if the pod has any container status with an image pull error.
// It returns the error message and a boolean indicating if an error was found.
func checkImagePullError(pod *corev1.Pod) (string, bool) {
	// Check all container statuses (both init containers and regular containers)
	for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		if status.State.Waiting != nil {
			reason := status.State.Waiting.Reason
			// Check for common image pull error reasons
			if reason == "ErrImagePull" || reason == "ImagePullBackOff" || reason == "InvalidImageName" {
				msg := status.State.Waiting.Message
				if msg == "" {
					msg = fmt.Sprintf("Container %s failed to pull image: %s", status.Name, reason)
				}
				return msg, true
			}
		}
	}
	return "", false
}

// updateCompositionWithImagePullError updates the composition with the image pull error message.
// It adds a result with severity "error" to the InFlightSynthesis.Results array.
func (p *podGarbageCollector) updateCompositionWithImagePullError(ctx context.Context, pod *corev1.Pod, errMsg string, logger logr.Logger) error {
	if pod.Labels == nil {
		return fmt.Errorf("pod has no labels")
	}

	compName := pod.Labels[compositionNameLabelKey]
	compNamespace := pod.Labels[compositionNamespaceLabelKey]
	synthUUID := pod.Labels[synthesisIDLabelKey]
	
	if compName == "" || compNamespace == "" || synthUUID == "" {
		return fmt.Errorf("pod is missing required labels")
	}

	// Get the composition
	comp := &apiv1.Composition{}
	comp.Name = compName
	comp.Namespace = compNamespace
	err := p.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if err != nil {
		return fmt.Errorf("fetching composition: %w", err)
	}

	// Ensure this is for the current in-flight synthesis
	if comp.Status.InFlightSynthesis == nil || comp.Status.InFlightSynthesis.UUID != synthUUID {
		return fmt.Errorf("composition no longer refers to this synthesis")
	}

	// Create a copy for patching
	original := comp.DeepCopy()
	
	// Add the error to the synthesis results
	formattedError := fmt.Sprintf("Failed to pull synthesizer image: %s", errMsg)
	comp.Status.InFlightSynthesis.Results = append(comp.Status.InFlightSynthesis.Results, apiv1.Result{
		Message:  formattedError,
		Severity: "error",
		Tags:     map[string]string{"source": "image-pull"},
	})
	
	// Update the composition status
	return p.client.Status().Patch(ctx, comp, client.MergeFrom(original))
}
