package aggregation

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
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
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

	next := c.aggregate(comp)
	if equality.Semantic.DeepEqual(next, comp.Status.Simplified) {
		return ctrl.Result{}, nil
	}
	comp.Status.Simplified = next
	if err := c.client.Status().Update(ctx, comp); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	logger.V(1).Info("wrote simplified composition status")
	return ctrl.Result{}, nil
}

func (c *compositionController) aggregate(comp *apiv1.Composition) *apiv1.SimplifiedStatus {
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
	if comp.Status.CurrentSynthesis == nil {
		return copy
	}

	for _, result := range comp.Status.CurrentSynthesis.Results {
		if result.Severity == krmv1.ResultSeverityError {
			copy.Error = result.Message
			break
		}
	}

	copy.Status = "Synthesizing"
	if comp.Status.CurrentSynthesis.Synthesized != nil {
		copy.Status = "Reconciling"
	}
	if comp.Status.CurrentSynthesis.Reconciled != nil {
		copy.Status = "NotReady"
	}
	if comp.Status.CurrentSynthesis.Ready != nil {
		copy.Status = "Ready"
	}
	if comp.Status.PendingResynthesis != nil {
		copy.Status = "PendingResynthesis"
	}

	return copy
}
