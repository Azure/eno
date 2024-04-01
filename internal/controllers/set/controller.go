package set

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

type compositionSetController struct {
	client client.Client
}

func NewCompositionSetController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.CompositionSet{}).
		Owns(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionSetController")).
		Complete(&compositionSetController{
			client: mgr.GetClient(),
		})
}

func (c *compositionSetController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	set := &apiv1.CompositionSet{}
	err := c.client.Get(ctx, req.NamespacedName, set)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger = logger.WithValues("compositionSetName", set.Name, "compositionSetNamespace", set.Namespace)
	expectedSynths := map[string]struct{}{}
	for _, syn := range set.Spec.Synthesizers {
		expectedSynths[syn.Name] = struct{}{}
	}

	existing := &apiv1.CompositionList{}
	err = c.client.List(ctx, existing) // TODO: INdex
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing existing compositions: %w", err)
	}

	existingBySynthName := map[string][]*apiv1.Composition{}
	for _, comp := range existing.Items {
		comp := comp
		existingBySynthName[comp.Spec.Synthesizer.Name] = append(existingBySynthName[comp.Spec.Synthesizer.Name], &comp)

		if _, ok := expectedSynths[comp.Spec.Synthesizer.Name]; ok || comp.DeletionTimestamp == nil {
			continue // should still exist, or already deleting
		}

		// Synth no longer exists in set - remove the composition
		err := c.client.Delete(ctx, &comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("cleaning up composition: %w", err)
		}
		logger.V(0).Info("deleted composition because its synthesizer was removed from the set", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
	}
	for _, comps := range existingBySynthName {
		if len(comps) < 2 {
			continue
		}

		// Oldest first
		// TODO: Test
		sort.Slice(comps, func(i, j int) bool { return comps[i].CreationTimestamp.Before(&comps[j].CreationTimestamp) })

		err := c.client.Delete(ctx, comps[0])
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting duplicate composition: %w", err)
		}
		logger.V(0).Info("deleted composition because it's a duplicate", "compositionName", comps[0].Name, "compositionNamespace", comps[0].Namespace)
	}

	// TODO: Set finalizer
	for _, synRef := range set.Spec.Synthesizers {
		synRef := synRef
		comp := &apiv1.Composition{}
		comp.Namespace = set.Namespace
		comp.GenerateName = synRef.Name + "-"
		comp.Spec.Bindings = set.Spec.Bindings
		comp.Spec.Synthesizer = synRef
		err = controllerutil.SetControllerReference(set, comp, c.client.Scheme())
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("setting composition's controller: %w", err)
		}

		// Diff and update if needed when the composition for this synthesizer already exists
		if existing, ok := existingBySynthName[synRef.Name]; ok {
			if equality.Semantic.DeepEqual(comp.Spec, existing[0].Spec) {
				continue // already matches
			}

			comp.ResourceVersion = existing[0].ResourceVersion
			err = c.client.Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating existing composition: %w", err)
			}
			logger.V(0).Info("updated composition because the set's spec changed", "compositionName", existing[0].Name, "compositionNamespace", existing[0].Namespace)
		}

		err = c.client.Create(ctx, comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("creating composition: %w", err)
		}
		logger.V(0).Info("created composition for the set", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
	}

	return ctrl.Result{}, nil
}
