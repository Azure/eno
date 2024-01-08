package cleanup

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TODO: Hold finalizer on synthesizer resources

type compositionController struct {
	client client.Client
}

func NewCompositionController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionCleanupController")).
		Complete(&compositionController{
			client: mgr.GetClient(),
		})
}

func (c *compositionController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", req.Name, "compositionNamespace", req.Namespace)

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}
	if comp.DeletionTimestamp == nil || !controllerutil.ContainsFinalizer(comp, "eno.azure.io/cleanup") {
		return ctrl.Result{}, nil // nothing to do
	}

	list := &apiv1.ResourceSliceList{}
	err = c.client.List(ctx, list, client.MatchingFields{
		manager.IdxResourceSlicesByComposition: comp.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing resource slices: %w", err)
	}
	if n := len(list.Items); n > 0 {
		return ctrl.Result{}, nil // slices still exist
	}

	controllerutil.RemoveFinalizer(comp, "eno.azure.io/cleanup")
	err = c.client.Update(ctx, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	logger.Info("removed finalizer from composition because none of its resource slices remain")
	return ctrl.Result{}, nil
}
