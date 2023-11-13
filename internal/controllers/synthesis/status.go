package synthesis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type statusController struct {
	client client.Client
}

// NewStatusController updates composition statuses as pods transition through states.
func NewStatusController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "statusController")).
		Complete(&statusController{
			client: mgr.GetClient(),
		})
}

func (c *statusController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	pod := &corev1.Pod{}
	err := c.client.Get(ctx, req.NamespacedName, pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting pod: %w", err))
	}
	if len(pod.OwnerReferences) == 0 || pod.OwnerReferences[0].Kind != "Composition" {
		logger.V(1).Info("skipping pod because it isn't owned by a composition")
		return ctrl.Result{}, nil
	}
	if pod.Annotations == nil {
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

	// Update composition status
	var (
		compGen, _ = strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
		synGen, _  = strconv.ParseInt(pod.Annotations["eno.azure.io/synthesizer-generation"], 10, 0)
	)

	statusIsOutOfSync := comp.Status.CurrentState == nil ||
		comp.Status.CurrentState.PodCreation == nil ||
		(comp.Status.CurrentState.ObservedSynthesizerGeneration == nil || *comp.Status.CurrentState.ObservedSynthesizerGeneration != synGen)

	if comp.Status.CurrentState.ObservedGeneration == compGen && statusIsOutOfSync {
		comp.Status.CurrentState.PodCreation = &pod.CreationTimestamp
		comp.Status.CurrentState.ObservedSynthesizerGeneration = &synGen

		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
		}
		logger.Info("updated synthesis status")
		return ctrl.Result{}, nil
	}

	// Remove the finalizer
	if controllerutil.RemoveFinalizer(pod, "eno.azure.io/cleanup") {
		logger.Info("removed pod finalizer")
		if err := c.client.Update(ctx, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing pod finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}
