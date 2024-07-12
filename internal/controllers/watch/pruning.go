package watch

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type pruningController struct {
	client client.Client
}

func (c *pruningController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	for i, ir := range comp.Status.InputRevisions {
		if hasBindingKey(comp, ir.Key) {
			continue
		}
		comp.Status.InputRevisions = append(comp.Status.InputRevisions[:i], comp.Status.InputRevisions[i+1:]...)
		err = c.client.Status().Update(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}

		logr.FromContextOrDiscard(ctx).V(1).Info("pruned old input revision from composition status", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", ir.Key)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func hasBindingKey(comp *apiv1.Composition, key string) bool {
	for _, b := range comp.Spec.Bindings {
		if b.Key == key {
			return true
		}
	}
	return false
}
