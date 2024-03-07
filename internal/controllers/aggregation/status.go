package aggregation

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
)

type statusController struct {
	client client.Client
}

func NewStatusController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "statusAggregationController")).
		Complete(&statusController{
			client: mgr.GetClient(),
		})
}

func (s *statusController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := s.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}
	if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil || (comp.Status.CurrentSynthesis.Ready != nil && comp.Status.CurrentSynthesis.Reconciled != nil) {
		return ctrl.Result{}, nil
	}

	ready := true
	reconciled := true
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := s.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting resource slice: %w", err)
		}

		// Status might be lagging behind
		if len(slice.Status.Resources) == 0 && len(slice.Spec.Resources) > 0 {
			ready = false
			reconciled = false
			break
		}

		for _, state := range slice.Status.Resources {
			// Sync
			if !state.Reconciled || (comp.DeletionTimestamp != nil && !state.Deleted) {
				reconciled = false
			}

			// Readiness
			if state.Ready == nil {
				ready = false
			}
		}
	}

	if (comp.Status.CurrentSynthesis.Reconciled != nil) == reconciled && (comp.Status.CurrentSynthesis.Ready != nil) == ready {
		return ctrl.Result{}, nil
	}

	now := metav1.Now()
	if ready {
		comp.Status.CurrentSynthesis.Ready = &now
	} else {
		comp.Status.CurrentSynthesis.Ready = nil
	}
	if reconciled {
		comp.Status.CurrentSynthesis.Reconciled = &now
	} else {
		comp.Status.CurrentSynthesis.Reconciled = nil
	}
	err = s.client.Status().Update(ctx, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
	}
	logger.Info("aggregated resource status into composition")

	return ctrl.Result{}, nil
}
