package synthesis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
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
	if !manager.PodReferencesComposition(pod) {
		// This shouldn't be common as the informer watch filters on Eno-managed pods using a selector
		return ctrl.Result{}, nil
	}
	if pod.Annotations == nil {
		logger.V(0).Info("synthesizer pod without any annotations was found - removing its finalizer")
		return c.removeFinalizer(ctx, pod)
	}

	var shouldRequeue bool
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		comp := &apiv1.Composition{}
		comp.Name = pod.GetLabels()[manager.CompositionNameLabelKey]
		comp.Namespace = pod.GetLabels()[manager.CompositionNamespaceLabelKey]
		err = c.client.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		if errors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("getting composition resource: %w", err)
		}
		logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

		var (
			compGen, _ = strconv.ParseInt(pod.Annotations["eno.azure.io/composition-generation"], 10, 0)
			synGen, _  = strconv.ParseInt(pod.Annotations["eno.azure.io/synthesizer-generation"], 10, 0)
		)
		logger = logger.WithValues("synthesizerGeneration", synGen, "compositionGeneration", compGen)

		if !shouldWriteStatus(comp, compGen, pod.CreationTimestamp) {
			return nil
		}

		if comp.Status.CurrentSynthesis == nil {
			comp.Status.CurrentSynthesis = &apiv1.Synthesis{}
		}
		comp.Status.CurrentSynthesis.PodCreation = &pod.CreationTimestamp
		comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration = synGen
		comp.Status.CurrentSynthesis.Attempts++

		if err := c.client.Status().Update(ctx, comp); err != nil {
			return fmt.Errorf("updating composition status: %w", err)
		}
		logger.V(0).Info("wrote synthesizer pod metadata to composition")

		shouldRequeue = true
		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if shouldRequeue {
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
	logger.V(0).Info("released synthesizer pod finalizer")
	return ctrl.Result{Requeue: true}, nil
}

func shouldWriteStatus(comp *apiv1.Composition, podCompGen int64, ctime metav1.Time) bool {
	current := comp.Status.CurrentSynthesis
	return current == nil || (current.ObservedCompositionGeneration == podCompGen && (current.PodCreation == nil || current.ObservedSynthesizerGeneration == 0 || !current.PodCreation.Equal(&ctime)))
}
