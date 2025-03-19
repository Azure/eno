package composition

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type compositionController struct {
	client client.Client
}

func NewController(mgr ctrl.Manager) error {
	c := &compositionController{
		client: mgr.GetClient(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionController")).
		Complete(c)
}

func (c *compositionController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition resource: %w", err))
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisID", comp.Status.GetCurrentSynthesisUUID())

	if comp.DeletionTimestamp != nil {
		return c.reconcileDeletedComposition(ctx, comp)
	}

	if controllerutil.AddFinalizer(comp, "eno.azure.io/cleanup") {
		err = c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating composition: %w", err)
		}
		logger.V(1).Info("added cleanup finalizer to composition")
		return ctrl.Result{}, nil
	}

	synth := &apiv1.Synthesizer{}
	synth.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(synth), synth)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting synthesizer: %w", err))
	}
	if synth != nil {
		logger = logger.WithValues("synthesizerName", synth.Name, "synthesizerGeneration", synth.Generation)
	}

	// Enforce the synthesis timeout period
	if syn := comp.Status.InFlightSynthesis; syn != nil && syn.Initialized != nil && synth.Spec.PodTimeout != nil {
		delta := time.Until(syn.Initialized.Time.Add(synth.Spec.PodTimeout.Duration))
		if delta > 0 {
			return ctrl.Result{RequeueAfter: delta}, nil
		}
		syn.Canceled = ptr.To(metav1.Now())
		if err := c.client.Status().Update(ctx, comp); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating compostion to reflect synthesis timeout: %w", err)
		}
		logger.V(0).Info("synthesis timed out")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (c *compositionController) reconcileDeletedComposition(ctx context.Context, comp *apiv1.Composition) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	syn := comp.Status.CurrentSynthesis

	if syn != nil {
		// Deletion increments the composition's generation, but the reconstitution cache is only invalidated
		// when the synthesized generation (from the status) changes, which will never happen because synthesis
		// is righly disabled for deleted compositions. We break out of this deadlock condition by updating
		// the status without actually synthesizing.
		if syn.ObservedCompositionGeneration != comp.Generation {
			comp.Status.CurrentSynthesis.ObservedCompositionGeneration = comp.Generation
			comp.Status.CurrentSynthesis.UUID = uuid.NewString()
			comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
			comp.Status.CurrentSynthesis.Reconciled = nil
			comp.Status.CurrentSynthesis.Ready = nil
			err := c.client.Status().Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating current composition generation: %w", err)
			}
			logger.V(0).Info("updated composition status to reflect deletion", "synthesisID", comp.Status.CurrentSynthesis.UUID)
			return ctrl.Result{}, nil
		}

		if syn.Reconciled == nil {
			logger.V(1).Info("refusing to remove composition finalizer because it is still being reconciled")
			return ctrl.Result{}, nil
		}
	}

	if controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}

		logger.V(0).Info("removed finalizer from composition")
	}

	return ctrl.Result{}, nil
}
