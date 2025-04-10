package synthesis

import (
	"context"
	"fmt"
	"time"

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
	if errors.IsNotFound(err) || pod.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get pod")
		return ctrl.Result{}, err
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

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		logger = logger.WithValues("reason", pod.Status.Phase)
		return ctrl.Result{}, p.deletePod(ctx, pod, logger)
	}

	return ctrl.Result{}, nil
}

func (p *podGarbageCollector) deletePod(ctx context.Context, pod *corev1.Pod, logger logr.Logger) error {
	if len(pod.Status.ContainerStatuses) > 0 {
		logger = logger.WithValues("restarts", pod.Status.ContainerStatuses[0].RestartCount)
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
