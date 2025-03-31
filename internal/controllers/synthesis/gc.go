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
	if err != nil || pod.DeletionTimestamp != nil {
		logger.Error(err, "failed to get pod or pod is marked for deletion")
		return ctrl.Result{}, client.IgnoreNotFound(err)
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
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)
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
	if exitTS := containerExitedTime(pod); exitTS != nil {
		logger = logger.WithValues("latency", exitTS.Sub(pod.CreationTimestamp.Time))
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		logger = logger.WithValues("restarts", pod.Status.ContainerStatuses[0].RestartCount)
	}
	err := p.client.Delete(ctx, pod, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}})
	if err != nil {
		return fmt.Errorf("deleting pod: %w", err)
	}
	logger.Info("deleted synthesizer pod")
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

func containerExitedTime(pod *corev1.Pod) (ts *time.Time) {
	for _, cont := range pod.Status.ContainerStatuses {
		if state := cont.LastTerminationState.Terminated; state != nil && (ts == nil || state.FinishedAt.Time.After(*ts)) {
			ts = &state.FinishedAt.Time
		}
	}
	return ts
}
