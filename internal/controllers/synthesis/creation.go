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

type podCreationController struct {
	config *Config
	client client.Client
}

func (c *podCreationController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	// Delete any pods already synthesizing this composition if they are no longer needed
	// i.e. the composition or synthesizer have been updated since their creation
	pods := &corev1.PodList{}
	err = c.client.List(ctx, pods, client.MatchingFields{
		compByPodIndex: comp.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing pods: %w", err)
	}
	if len(pods.Items) > 0 {
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				logger.V(1).Info("pod is pending deletion - skipping", "podName", pod.Name)
				continue
			}

			if true { // TODO: Compare with the current comp/synth
				continue
			}

			if err := c.client.Delete(ctx, &pod); err != nil {
				return ctrl.Result{}, fmt.Errorf("deleting old pod: %w", err)
			}
			logger.Info("delete useless pod", "podName", pod.Name)
		}
		return ctrl.Result{}, nil
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

	// If we made it this far it's safe to create a pod
	pod := newPod(c.config, c.client.Scheme(), comp, syn)
	err = c.client.Create(ctx, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreAlreadyExists(fmt.Errorf("creating pod: %w", err))
	}
	logger.Info("created pod", "podName", pod.Name)

	return ctrl.Result{}, nil
}

func (c *podCreationController) shouldDeferForRollingUpdate(ctx context.Context, comp *apiv1.Composition, syn *apiv1.Synthesizer) (*ctrl.Result, error) {
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
