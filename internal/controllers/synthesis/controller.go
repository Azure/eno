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

const compBySynIndex = ".spec.synthesizer"

type Config struct {
	WrapperImage    string
	JobSA           string
	MaxRestarts     int32
	Timeout         time.Duration
	RolloutCooldown time.Duration
}

type Controller struct {
	config *Config
	client client.Client
	logger logr.Logger
}

func NewController(mgr ctrl.Manager, cfg *Config) error {
	c := &Controller{
		config: cfg,
		client: mgr.GetClient(),
		logger: mgr.GetLogger(),
	}

	err := mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Composition{}, compBySynIndex, func(o client.Object) []string {
		comp := o.(*apiv1.Composition)
		return []string{comp.Spec.Synthesizer.Name}
	})
	if err != nil {
		return err
	}

	// IMPORTANT: The manager's pod informer should be filtered on a label present on pods created by this controller to avoid caching all pods on the cluster
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&corev1.Pod{}).
		Watches(&apiv1.Synthesizer{}, &synthEventHandler{ctrl: c}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
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

	// Handle existing pods
	pod := newPod(c.config, c.client.Scheme(), comp, syn)
	current := &corev1.Pod{}
	logger := c.logger.WithValues("composition", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesizer", syn.Name, "synthesizerGeneration", syn.Generation)

	err = c.client.Get(ctx, client.ObjectKeyFromObject(pod), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current pod: %w", err)
	}
	if err == nil {
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
				PodCreation:                   current.CreationTimestamp,
			}
			if err := c.client.Status().Update(ctx, comp); err != nil {
				return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
			}
			logger.V(1).Info("added this synthesis to the composition status")
			return ctrl.Result{}, nil
		}

		// Clean up if the pod is no longer needed
		if current.DeletionTimestamp == nil && c.shouldDeletePod(current) {
			err = c.client.Delete(ctx, current)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("error deleting pod: %w", err)
			}
			logger.Info("deleted synthesis pod")
		}

		// At this point we know the pod is still running.
		// Poll periodically to check if has timed out.
		return ctrl.Result{RequeueAfter: c.config.Timeout}, nil
	}

	// Skip cases in which the GeneratedResources have already been created
	compInSync := comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedGeneration == comp.Generation
	synthInSync := comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
	if compInSync && synthInSync {
		return ctrl.Result{}, nil // already in sync
	}

	// Slow-roll synthesizer changes across referencing compositions only when the composition itself hasn't changed
	if compInSync && !synthInSync {
		res, err := c.shouldDeferForRollingUpdate(ctx, comp, syn)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res != nil {
			logger.Info(fmt.Sprintf("deferring synthesis for %s because another composition is within the cooldown window", res.RequeueAfter))
			return *res, nil
		}
		logger.Info("re-synthesizing composition because the synthesizer configuration changed")
	}

	// At this point the pod is missing another shouldn't be
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating synthesis pod: %w", err))
	}
	logger.Info("created pod")

	return ctrl.Result{}, nil
}

func (c *Controller) shouldDeferForRollingUpdate(ctx context.Context, comp *apiv1.Composition, syn *apiv1.Synthesizer) (*ctrl.Result, error) {
	list := &apiv1.CompositionList{}
	if err := c.client.List(ctx, list, client.MatchingFields{
		compBySynIndex: syn.Name,
	}); err != nil {
		return nil, err
	}

	for _, item := range list.Items {
		if item.Name == comp.Name && item.Namespace == comp.Namespace {
			continue
		}
		// We can safely ignore compositions that don't have status yet since they will be reconciled soon
		if item.Status.CurrentState == nil {
			continue
		}
		sinceLastGeneration := time.Since(item.Status.CurrentState.PodCreation.Time)
		remainingCooldown := c.config.RolloutCooldown - sinceLastGeneration
		if remainingCooldown > 0 {
			return &ctrl.Result{Requeue: true, RequeueAfter: remainingCooldown}, nil
		}
	}

	return nil, nil
}

func (c *Controller) shouldDeletePod(pod *corev1.Pod) bool {
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
