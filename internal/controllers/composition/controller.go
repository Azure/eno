package composition

import (
	"context"
	"fmt"
	"time"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/inputs"
	"github.com/Azure/eno/internal/manager"
)

const (
	AKSComponentLabel     = "aks.azure.com/component-type"
	OverlayComponentLabel = "eno.azure.io/overlaymgr-component-type"
	addOnLabelValue       = "addon"
)

type compositionController struct {
	client     client.Client
	podTimeout time.Duration
}

func NewController(mgr ctrl.Manager, podTimeout time.Duration) error {
	c := &compositionController{
		client:     mgr.GetClient(),
		podTimeout: podTimeout,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WatchesRawSource(source.Kind(mgr.GetCache(), &apiv1.Synthesizer{}, c.newSynthEventHandler())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionController")).
		Complete(c)
}

func (c *compositionController) newSynthEventHandler() handler.TypedEventHandler[*apiv1.Synthesizer, reconcile.Request] {
	fn := func(ctx context.Context, synth *apiv1.Synthesizer) (reqs []reconcile.Request) {
		logger := logr.FromContextOrDiscard(ctx)

		list := &apiv1.CompositionList{}
		err := c.client.List(ctx, list, client.MatchingFields{
			manager.IdxCompositionsBySynthesizer: synth.Name,
		})
		if err != nil {
			logger.Error(err, "failed to list compositions for synthesizer")
			return nil
		}
		for _, comp := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&comp)})
		}
		return reqs
	}
	return handler.TypedEnqueueRequestsFromMapFunc(fn)
}

func (c *compositionController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisUUID", comp.Status.GetCurrentSynthesisUUID(),
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())

	if comp.DeletionTimestamp != nil {
		return c.reconcileDeletedComposition(ctx, comp)
	}

	if controllerutil.AddFinalizer(comp, "eno.azure.io/cleanup") {
		err = c.client.Update(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to update composition")
			return ctrl.Result{}, err
		}
		logger.Info("added cleanup finalizer to composition")
		return ctrl.Result{}, nil
	}

	synth := &apiv1.Synthesizer{}
	synth.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(synth), synth)
	if errors.IsNotFound(err) {
		logger.Info(fmt.Sprintf("synthesizer not found for composition[%s], namespace[%s], synthName[%s]", comp.GetName(), comp.GetNamespace(), comp.Spec.Synthesizer.Name))
		synth = nil
		err = nil
	}
	if err != nil {
		logger.Error(err, "failed to get synthesizer")
		return ctrl.Result{}, err
	}
	if synth != nil {
		logger = logger.WithValues("synthesizerName", synth.Name, "synthesizerGeneration", synth.Generation)
	}
	ctx = logr.NewContext(ctx, logger)

	// Write the simplified status
	modified, err := c.reconcileSimplifiedStatus(ctx, synth, comp)
	if err != nil {
		logger.Error(err, "failed to reconcile simplified status")
		return ctrl.Result{}, err
	}
	if modified || synth == nil {
		return ctrl.Result{}, nil
	}

	// Enforce the synthesis timeout period
	if syn := comp.Status.InFlightSynthesis; syn != nil && syn.Canceled == nil && syn.Initialized != nil {
		delta := time.Until(syn.Initialized.Time.Add(c.podTimeout))
		if delta > 0 {
			return ctrl.Result{RequeueAfter: delta}, nil
		}
		syn.Canceled = ptr.To(metav1.Now())
		if err := c.client.Status().Update(ctx, comp); err != nil {
			logger.Error(err, "failed to update composition status to reflect synthesis timeout")
			return ctrl.Result{}, err
		}
		logger.Error(nil, "synthesis timed out")
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
				logger.Error(err, "failed to update current composition generation")
				return ctrl.Result{}, err
			}
			logger.Info("updated composition status to reflect deletion", "synthesisUUID", comp.Status.CurrentSynthesis.UUID)
			return ctrl.Result{}, nil
		}

		if syn.Reconciled == nil {
			// If this is an addon composition whose owning Symphony is already gone,
			// force-remove the finalizer so the composition doesn't get stuck forever.
			if c.shouldForceRemoveFinalizer(ctx, comp) {
				logger.Info("force removing finalizer for addon composition because owning symphony is gone",
					"compositionName", comp.Name, "compositionNamespace", comp.Namespace)
			} else {
				logger.Info("refusing to remove composition finalizer because it is still being reconciled")
				return ctrl.Result{}, nil
			}
		}
	}

	if controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}

		logger.Info("removed finalizer from composition")
	}

	return ctrl.Result{}, nil
}

func (c *compositionController) reconcileSimplifiedStatus(ctx context.Context, synth *apiv1.Synthesizer, comp *apiv1.Composition) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	next := buildSimplifiedStatus(synth, comp)
	if equality.Semantic.DeepEqual(next, comp.Status.Simplified) {
		return false, nil
	}

	logger.Info("composition status changed", "previousStatus", comp.Status.Simplified, "currentStatus", next)
	copy := comp.DeepCopy()
	copy.Status.Simplified = next
	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(comp)); err != nil {
		return false, fmt.Errorf("patching simplified status: %w", err)
	}
	logger.Info("sucessfully updated status for composition")
	return true, nil
}

// shouldForceRemoveFinalizer returns true if and only if the composition is an addon
// (has label aks.azure.com/component-type=addon or eno.azure.io/overlaymgr-component-type=addon)
// AND the owning Symphony no longer exists. If the component type is anything other than "addon"
// (e.g. "ccp"), or if the Symphony still exists, this returns false.
func (c *compositionController) shouldForceRemoveFinalizer(ctx context.Context, comp *apiv1.Composition) bool {
	logger := logr.FromContextOrDiscard(ctx)

	if !isAddonComposition(comp) {
		return false
	}

	// Find the owning Symphony from the owner references.
	ownerRefs := comp.GetOwnerReferences()
	var symphName string
	for _, ref := range ownerRefs {
		if ref.Kind == "Symphony" {
			symphName = ref.Name
			break
		}
	}
	if symphName == "" {
		logger.Info("composition has no Symphony owner reference, skip force removing finalizer",
			"compositionName", comp.GetName(), "compositionNamespace", comp.GetNamespace())
		return false
	}

	// Check if the owning Symphony still exists.
	symph := &apiv1.Symphony{}
	symphKey := types.NamespacedName{
		Namespace: comp.GetNamespace(),
		Name:      symphName,
	}
	logger.Info("checking if owning symphony still exists",
		"symphonyName", symphName, "symphonyNamespace", comp.GetNamespace())
	err := c.client.Get(ctx, symphKey, symph)
	if errors.IsNotFound(err) {
		logger.Info("owning symphony is gone, force removing finalizer",
			"compositionName", comp.GetName(), "compositionNamespace", comp.GetNamespace(),
			"symphonyName", symphName)
		return true
	}
	if err != nil {
		// Transient error — don't force remove; we'll retry on the next reconcile.
		logger.Error(err, "failed to check if owning symphony exists, skip force removing finalizer",
			"symphonyName", symphName)
		return false
	}

	logger.Info("symphony still exists, skip force removing finalizer",
		"compositionName", comp.GetName(), "symphonyName", symphName)
	return false
}

// isAddonComposition returns true if the composition has at least one of the component-type labels
// set to "addon", and neither label is set to a non-addon value.
func isAddonComposition(comp *apiv1.Composition) bool {
	labels := comp.GetLabels()
	if labels == nil {
		return false
	}

	aksLabel, hasAKS := labels[AKSComponentLabel]
	overlayLabel, hasOverlay := labels[OverlayComponentLabel]

	if !hasAKS && !hasOverlay {
		return false
	}

	// If a label is present, it must be "addon". Any non-addon value (e.g. "ccp") disqualifies.
	if hasAKS && aksLabel != addOnLabelValue {
		return false
	}
	if hasOverlay && overlayLabel != addOnLabelValue {
		return false
	}

	return true
}

func buildSimplifiedStatus(synth *apiv1.Synthesizer, comp *apiv1.Composition) *apiv1.SimplifiedStatus {
	status := &apiv1.SimplifiedStatus{}
	current := comp.Status.Simplified

	if comp.DeletionTimestamp != nil {
		status.Status = "Deleting"
		return status
	}
	if synth == nil {
		status.Status = "MissingSynthesizer"
		return status
	}

	if syn := comp.Status.InFlightSynthesis; syn != nil {
		for _, result := range syn.Results {
			if result.Severity == krmv1.ResultSeverityError {
				status.Error = result.Message
				break
			}
		}

		if syn.Canceled != nil {
			if status.Error == "" {
				status.Error = "Timeout"
			}
			status.Status = "SynthesisBackoff"
			return status
		}

		status.Status = "Synthesizing"
		return status
	}

	if !inputs.Exist(synth, comp) {
		status.Status = "MissingInputs"
		return status
	}
	if inputs.OutOfLockstep(synth, comp, comp.Status.InputRevisions) {
		status.Status = "MismatchedInputs"
	}

	if comp.Status.CurrentSynthesis == nil && comp.Status.InFlightSynthesis == nil {
		status.Status = "PendingSynthesis"
		return status
	}
	if syn := comp.Status.CurrentSynthesis; syn.Ready != nil {
		status.Status = "Ready"
		return status
	}
	if syn := comp.Status.CurrentSynthesis; syn.Reconciled != nil {
		status.Status = "NotReady"
		return status
	}
	if syn := comp.Status.CurrentSynthesis; syn != nil && syn.Reconciled == nil {
		status.Status = "Reconciling"
		if current != nil {
			// Preserve any reconciliation error written by the resource slice controller
			status.Error = current.Error
		}
		return status
	}

	status.Status = "Unknown"
	return status
}
