package watch

import (
	"context"
	"fmt"
	"sort"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// discoveryController watches compositions/synthesizers to "discover" resource bindings.
// Each discovered binding results in creation of a ReferencedResource CR to track its state.
// This controller is responsible for the full lifecycle of those CRs including cleanup.
type discoveryController struct {
	client client.Client
}

func (c *discoveryController) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	comps := &apiv1.CompositionList{}
	err := c.client.List(ctx, comps)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	// Reconcile "forwards" i.e. create missing ReferencedResources
	activeBindings := map[apiv1.InputResource]struct{}{}
	for _, comp := range comps.Items {
		if comp.Spec.Synthesizer.Name == "" {
			continue // don't bother - we'll just get a 404 anyway
		}

		synth := &apiv1.Synthesizer{}
		err = c.client.Get(ctx, types.NamespacedName{Name: comp.Spec.Synthesizer.Name}, synth)
		if errors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting synthesizer: %w", err)
		}

		for _, binding := range comp.Spec.Bindings {
			ref := synth.GetRef(binding.Key)
			if ref == nil {
				continue // it's fine for compositions to bind to non-existant refs
			}

			ir := apiv1.InputResource{
				Name:      binding.Resource.Name,
				Namespace: binding.Resource.Namespace,
				Group:     ref.Resource.Group,
				Kind:      ref.Resource.Kind,
			}

			changed, err := c.reconcileReferencedResource(ctx, &ir)
			if err != nil {
				return ctrl.Result{}, err
			}
			if changed {
				return ctrl.Result{}, nil
			}
			activeBindings[ir] = struct{}{}
		}
	}

	// Reconcile "backwards" i.e. delete unused ReferencedResources
	rrl := &apiv1.ReferencedResourceList{}
	err = c.client.List(ctx, rrl)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing referenced resource CRs: %w", err)
	}
	for _, rl := range rrl.Items {
		ir := rl.Spec.Input
		if _, ok := activeBindings[ir]; ok {
			continue // still active
		}

		logger := logr.FromContextOrDiscard(ctx).WithValues("group", ir.Group, "kind", ir.Kind, "name", ir.Name, "namespace", ir.Namespace)
		err := c.client.Delete(ctx, &rl)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting referenced resource CR: %w", err)
		}
		logger.Info("cleaned up unused referenced resource CR")
	}

	return ctrl.Result{}, nil
}

func (c *discoveryController) reconcileReferencedResource(ctx context.Context, ir *apiv1.InputResource) (bool, error) {
	rrl := &apiv1.ReferencedResourceList{}
	err := c.client.List(ctx, rrl, client.MatchingFields{
		manager.IdxReferencedResourcesByRef: manager.ReferencedResourceIdxValueFromInputResource(ir),
	})
	if err != nil {
		return false, fmt.Errorf("listing referenced resource CRs: %w", err)
	}

	logger := logr.FromContextOrDiscard(ctx).WithValues("group", ir.Group, "kind", ir.Kind, "name", ir.Name, "namespace", ir.Namespace)

	// Creation
	if len(rrl.Items) == 0 {
		rr := &apiv1.ReferencedResource{}
		rr.GenerateName = "resource-"
		rr.Spec.Input = *ir

		err := c.client.Create(ctx, rr)
		if err != nil {
			return false, fmt.Errorf("creating referenced resource CR: %w", err)
		}

		logger.V(1).Info("created referenced resource CR to track bound resource")
		return true, nil
	}

	// Prune in case we inadvertently created multiple CRs for a single resource
	if len(rrl.Items) > 1 {
		sort.Slice(rrl.Items, func(i, j int) bool {
			return rrl.Items[i].CreationTimestamp.Before(&rrl.Items[j].CreationTimestamp)
		})

		err := c.client.Delete(ctx, &rrl.Items[0])
		if err != nil {
			return false, fmt.Errorf("pruning extra CR: %w", err)
		}

		logger.V(1).Info("pruned duplicate CR for bound resource")
		return true, nil
	}

	return false, nil
}
