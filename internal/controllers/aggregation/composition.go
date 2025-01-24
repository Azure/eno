package aggregation

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type compositionController struct {
	client client.Client
}

func NewCompositionController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionAggregationController")).
		Complete(&compositionController{
			client: mgr.GetClient(),
		})
}

func (c *compositionController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	synth := &apiv1.Synthesizer{}
	err = c.client.Get(ctx, types.NamespacedName{Name: comp.Spec.Synthesizer.Name}, synth)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	next := c.aggregate(synth, comp)
	if equality.Semantic.DeepEqual(next, comp.Status.Simplified) {
		return ctrl.Result{}, nil
	}
	copy := comp.DeepCopy()
	copy.Status.Simplified = next
	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(comp)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *compositionController) aggregate(synth *apiv1.Synthesizer, comp *apiv1.Composition) *apiv1.SimplifiedStatus {
	copy := comp.Status.Simplified.DeepCopy()
	if copy == nil {
		copy = &apiv1.SimplifiedStatus{}
	}

	if comp.DeletionTimestamp != nil {
		copy.Status = "Deleting"
		return copy
	}

	copy.Status = "PendingSynthesis"
	copy.Error = ""
	if !comp.InputsExist(synth) {
		copy.Status = "MissingInputs"
	}
	if comp.Status.CurrentSynthesis == nil {
		return copy
	}

	for _, result := range comp.Status.CurrentSynthesis.Results {
		if result.Severity == krmv1.ResultSeverityError {
			copy.Error = result.Message
			break
		}
	}

	// Fall back to using the first warning as the error message if no terminal error is given.
	// It's still useful to see error messages in the summary - even if they aren't fatal.
	if copy.Error == "" {
		for _, result := range comp.Status.CurrentSynthesis.Results {
			if result.Severity == krmv1.ResultSeverityWarning {
				copy.Error = result.Message
				break
			}
		}
	}

	copy.Status = "Synthesizing"
	if !comp.InputsExist(synth) {
		copy.Status = "MissingInputs"
	}
	if comp.Status.CurrentSynthesis.Synthesized != nil {
		copy.Status = "Reconciling"
	}
	if comp.Status.CurrentSynthesis.Reconciled != nil {
		copy.Status = "NotReady"
	}
	if comp.Status.CurrentSynthesis.Ready != nil {
		copy.Status = "Ready"
	}
	if comp.InputsOutOfLockstep(synth) {
		copy.Status = "MismatchedInputs"
	}

	return copy
}
