package replication

import (
	"context"
	"fmt"
	"sort"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type symphonyController struct {
	client client.Client
}

// TODO: Avoid conflicts

func NewCompositionSetController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Symphony{}).
		Owns(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "symphonyReplicationController")).
		Complete(&symphonyController{
			client: mgr.GetClient(),
		})
}

func (c *symphonyController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	symph := &apiv1.Symphony{}
	err := c.client.Get(ctx, req.NamespacedName, symph)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger = logger.WithValues("compositionSetName", symph.Name, "compositionSetNamespace", symph.Namespace)
	ctx = logr.NewContext(ctx, logger)

	existing := &apiv1.CompositionList{}
	err = c.client.List(ctx, existing, client.MatchingFields{
		manager.IdxCompositionsBySymphony: symph.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing existing compositions: %w", err)
	}

	// Hold a finalizer
	if controllerutil.AddFinalizer(symph, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, symph)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	existingBySynthName, err := c.reconcileReverse(ctx, symph, existing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if symph.DeletionTimestamp == nil {
		err = c.reconcileForward(ctx, symph, existingBySynthName)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Release the finalizer when no compositions exists
	if symph.DeletionTimestamp != nil {
		if len(existing.Items) > 0 {
			return ctrl.Result{}, nil // wait for composition deletion
		}
		if controllerutil.RemoveFinalizer(symph, "eno.azure.io/cleanup") {
			err := c.client.Update(ctx, symph)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
	}

	return ctrl.Result{}, nil
}

func (c *symphonyController) reconcileReverse(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (map[string][]*apiv1.Composition, error) {
	logger := logr.FromContextOrDiscard(ctx)

	expectedSynths := map[string]struct{}{}
	for _, syn := range symph.Spec.Synthesizers {
		expectedSynths[syn.Name] = struct{}{}
	}

	existingBySynthName := map[string][]*apiv1.Composition{}
	for _, comp := range comps.Items {
		comp := comp
		existingBySynthName[comp.Spec.Synthesizer.Name] = append(existingBySynthName[comp.Spec.Synthesizer.Name], &comp)

		if _, ok := expectedSynths[comp.Spec.Synthesizer.Name]; (ok || comp.DeletionTimestamp == nil) && symph.DeletionTimestamp == nil {
			continue // should still exist, or already deleting
		}
		err := c.client.Delete(ctx, &comp)
		if err != nil {
			return nil, fmt.Errorf("cleaning up composition: %w", err)
		}
		logger.V(0).Info("deleted composition because its synthesizer was removed from the set", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
	}

	for _, comps := range existingBySynthName {
		if len(comps) < 2 {
			continue
		}

		sort.Slice(comps, func(i, j int) bool { return comps[i].CreationTimestamp.Before(&comps[j].CreationTimestamp) })

		err := c.client.Delete(ctx, comps[0])
		if err != nil {
			return nil, fmt.Errorf("deleting duplicate composition: %w", err)
		}
		logger.V(0).Info("deleted composition because it's a duplicate", "compositionName", comps[0].Name, "compositionNamespace", comps[0].Namespace)
	}

	return existingBySynthName, nil
}

func (c *symphonyController) reconcileForward(ctx context.Context, symph *apiv1.Symphony, existingBySynthName map[string][]*apiv1.Composition) error {
	logger := logr.FromContextOrDiscard(ctx)

	for _, synRef := range symph.Spec.Synthesizers {
		synRef := synRef
		comp := &apiv1.Composition{}
		comp.Namespace = symph.Namespace
		comp.GenerateName = synRef.Name + "-"
		comp.Spec.Bindings = symph.Spec.Bindings
		comp.Spec.Synthesizer = synRef
		err := controllerutil.SetControllerReference(symph, comp, c.client.Scheme())
		if err != nil {
			return fmt.Errorf("setting composition's controller: %w", err)
		}

		// Diff and update if needed when the composition for this synthesizer already exists
		if existings, ok := existingBySynthName[synRef.Name]; ok {
			existing := existings[0]
			if equality.Semantic.DeepEqual(comp.Spec, existing.Spec) {
				continue // already matches
			}

			existing.Spec = comp.Spec
			err = c.client.Update(ctx, existing)
			if err != nil {
				return fmt.Errorf("updating existing composition: %w", err)
			}
			logger.V(0).Info("updated composition because the set's spec changed", "compositionName", existing.Name, "compositionNamespace", existing.Namespace)
		}

		err = c.client.Create(ctx, comp)
		if err != nil {
			return fmt.Errorf("creating composition: %w", err)
		}
		logger.V(0).Info("created composition for the set", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
	}

	return nil
}
