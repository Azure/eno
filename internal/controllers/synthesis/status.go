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
		// This shouldn't be common as the informer watch filters on Eno-managed pods using a selector
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
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
	}
	logger = logger.WithValues("synthesizerName", syn.Name)

	// Update composition status
	var (
		compGen, _ = strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
		synGen, _  = strconv.ParseInt(pod.Annotations["eno.azure.io/synthesizer-generation"], 10, 0)
	)
	logger.WithValues("synthesizerGeneration", synGen, "compositionGeneration", compGen)
	if statusIsOutOfSync(comp, compGen, synGen) {
		comp.Status.CurrentState.PodCreation = &pod.CreationTimestamp
		comp.Status.CurrentState.ObservedSynthesizerGeneration = synGen

		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
		}
		logger.V(1).Info("wrote synthesizer pod metadata to composition")
		return ctrl.Result{}, nil
	}

	// Remove the finalizer
	if controllerutil.RemoveFinalizer(pod, "eno.azure.io/cleanup") {
		if err := c.client.Update(ctx, pod); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing pod finalizer: %w", err)
		}
		logger.V(1).Info("synthesizer pod can safely be deleted now that its metadata has been captured")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func statusIsOutOfSync(comp *apiv1.Composition, podCompGen, podSynGen int64) bool {
	// TODO: Unit tests, make sure to cover the pod creation latching logic
	// TODO: Do we need to also check against the previous state? Do we swap if the current state is still being synthesized? Should we?
	return (comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedCompositionGeneration == podCompGen) &&
		(comp.Status.CurrentState.PodCreation == nil || comp.Status.CurrentState.ObservedSynthesizerGeneration != podSynGen)
}
