package synthesis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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
		// This shouldn't be common as the informer watch filters on Eno-managed pods using a selector
		return ctrl.Result{}, nil
	}
	if pod.Annotations == nil {
		logger.V(1).Info("synthesizer pod without any annotations was found - removing its finalizer")
		return c.removeFinalizer(ctx, pod)
	}

	comp := &apiv1.Composition{}
	comp.Name = pod.OwnerReferences[0].Name
	comp.Namespace = pod.Namespace
	err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	if errors.IsNotFound(err) {
		logger.V(1).Info("composition was deleted unexpectedly - releasing synthesizer pod")
		return c.removeFinalizer(ctx, pod)
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting composition resource: %w", err)
	}
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

	// Update composition status
	var (
		compGen, _ = strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
		synGen, _  = strconv.ParseInt(pod.Annotations["eno.azure.io/synthesizer-generation"], 10, 0)
	)
	logger.WithValues("synthesizerGeneration", synGen, "compositionGeneration", compGen)
	if shouldWriteStatus(comp, compGen) {
		if comp.Status.CurrentSynthesis == nil {
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
		}
		comp.Status.CurrentSynthesis.PodCreation = &pod.CreationTimestamp
		comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration = synGen

		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
		}
		logger.V(1).Info("wrote synthesizer pod metadata to composition")
		return ctrl.Result{Requeue: true}, nil
	}

	return c.removeFinalizer(ctx, pod)
}

func (c *statusController) removeFinalizer(ctx context.Context, pod *corev1.Pod) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	if !controllerutil.RemoveFinalizer(pod, "eno.azure.io/cleanup") {
		return ctrl.Result{}, nil
	}

	if err := c.client.Update(ctx, pod); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing pod finalizer: %w", err)
	}
	logger.V(1).Info("synthesizer pod can safely be deleted")
	return ctrl.Result{Requeue: true}, nil
}

func shouldWriteStatus(comp *apiv1.Composition, podCompGen int64) bool {
	current := comp.Status.CurrentSynthesis
	return current == nil || (current.ObservedCompositionGeneration == podCompGen && (current.PodCreation == nil || current.ObservedSynthesizerGeneration == 0))
}
