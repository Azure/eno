package reconciliation

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// TODO: This needs to block until deletes have been sent somehow

type StatusAggregationController struct {
	client client.Client
}

func NewStatusAggregationController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "statusController")).
		Complete(&StatusAggregationController{
			client: mgr.GetClient(),
		})
}

func (s *StatusAggregationController) Name() string { return "statusAggregationController" }

func (s *StatusAggregationController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := s.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
	}
	if comp.Status.CurrentState == nil || comp.Status.CurrentState.Synced {
		return ctrl.Result{}, nil
	}

	sliceList := &apiv1.ResourceSliceList{}
	if err := s.client.List(ctx, sliceList); err != nil {
		return ctrl.Result{}, err
	}
	for _, slice := range sliceList.Items {
		for _, res := range slice.Status.Resources {
			if !res.Reconciled {
				return ctrl.Result{}, nil // not ready yet
			}
		}
	}

	comp.Status.CurrentState.Ready = true
	return ctrl.Result{}, s.client.Status().Update(ctx, comp)
}
