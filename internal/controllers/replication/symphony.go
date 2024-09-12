package replication

import (
	"context"
	"fmt"
	"slices"
	"sort"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type symphonyController struct {
	client client.Client
}

func NewSymphonyController(mgr ctrl.Manager) error {
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
	logger = logger.WithValues("symphonyName", symph.Name, "symphonyNamespace", symph.Namespace)
	ctx = logr.NewContext(ctx, logger)

	existing := &apiv1.CompositionList{}
	err = c.client.List(ctx, existing, client.InNamespace(symph.Namespace), client.MatchingFields{
		manager.IdxCompositionsBySymphony: symph.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing existing compositions: %w", err)
	}

	modified, err := c.syncStatus(ctx, symph, existing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if modified {
		return ctrl.Result{}, nil
	}

	// Hold a finalizer
	if !controllerutil.ContainsFinalizer(symph, "eno.azure.io/cleanup") {
		copy := symph.DeepCopy()
		copy.Finalizers = append(copy.Finalizers, "eno.azure.io/cleanup")
		err := c.client.Patch(ctx, copy, client.MergeFrom(symph))
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// We reconcile both "forward" and in "reverse" i.e. creating/updating and deleting.
	// The two stages are broken up for the sake of minimizing and documenting state flowing between them.
	// Any changes cause the controller to return early and catch the next watch event as is expected from controllers.
	existingBySynthName, modified, err := c.reconcileReverse(ctx, symph, existing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if modified {
		return ctrl.Result{}, nil
	}
	if symph.DeletionTimestamp == nil {
		modified, err := c.reconcileForward(ctx, symph, existingBySynthName)
		if err != nil {
			return ctrl.Result{}, err
		}
		if modified {
			return ctrl.Result{}, nil
		}
	}

	// Release the finalizer when no compositions exists
	if symph.DeletionTimestamp != nil {
		if len(existing.Items) > 0 {
			return ctrl.Result{}, nil // wait for composition deletion
		}
		if controllerutil.ContainsFinalizer(symph, "eno.azure.io/cleanup") {
			copy := symph.DeepCopy()
			controllerutil.RemoveFinalizer(copy, "eno.azure.io/cleanup")
			err := c.client.Patch(ctx, copy, client.MergeFrom(symph))
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
	}

	return ctrl.Result{}, nil
}

func (c *symphonyController) reconcileReverse(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (map[string][]*apiv1.Composition, bool, error) {
	logger := logr.FromContextOrDiscard(ctx)

	expectedSynths := map[string]struct{}{}
	for _, variation := range symph.Spec.Variations {
		expectedSynths[variation.Synthesizer.Name] = struct{}{}
	}

	// Delete compositions when their synth has been removed from the symphony
	existingBySynthName := map[string][]*apiv1.Composition{}
	for _, comp := range comps.Items {
		comp := comp
		existingBySynthName[comp.Spec.Synthesizer.Name] = append(existingBySynthName[comp.Spec.Synthesizer.Name], &comp)

		if _, ok := expectedSynths[comp.Spec.Synthesizer.Name]; ok && symph.DeletionTimestamp == nil {
			continue // should still exist
		}
		if comp.DeletionTimestamp != nil {
			continue // already deleting
		}

		err := c.client.Delete(ctx, &comp)
		if err != nil {
			return nil, false, fmt.Errorf("cleaning up composition: %w", err)
		}

		logger.V(0).Info("deleted composition because its variation was removed from the symphony", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
		return existingBySynthName, true, nil
	}

	// Delete any duplicates we may have created in the past - leave the oldest one
	for _, comps := range existingBySynthName {
		if len(comps) < 2 {
			continue
		}

		sort.Slice(comps, func(i, j int) bool { return comps[i].CreationTimestamp.Before(&comps[j].CreationTimestamp) })

		err := c.client.Delete(ctx, comps[0])
		if err != nil {
			return nil, false, fmt.Errorf("deleting duplicate composition: %w", err)
		}

		logger.V(0).Info("deleted composition because it's a duplicate", "compositionName", comps[0].Name, "compositionNamespace", comps[0].Namespace)
		return existingBySynthName, true, nil
	}

	return existingBySynthName, false, nil
}

func (c *symphonyController) reconcileForward(ctx context.Context, symph *apiv1.Symphony, existingBySynthName map[string][]*apiv1.Composition) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)

	for _, variation := range symph.Spec.Variations {
		variation := variation
		comp := &apiv1.Composition{}
		comp.Namespace = symph.Namespace
		comp.GenerateName = variation.Synthesizer.Name + "-"
		comp.Spec.Bindings = getBindings(symph, &variation)
		comp.Spec.Synthesizer = variation.Synthesizer
		comp.Spec.SynthesisEnv = symph.Spec.SynthesisEnv
		comp.Labels = variation.Labels
		comp.Annotations = variation.Annotations
		err := controllerutil.SetControllerReference(symph, comp, c.client.Scheme())
		if err != nil {
			return false, fmt.Errorf("setting composition's controller: %w", err)
		}

		// Diff and update if needed when the composition for this synthesizer already exists
		if existings, ok := existingBySynthName[variation.Synthesizer.Name]; ok {
			existing := existings[0]
			if equality.Semantic.DeepEqual(comp.Spec, existing.Spec) && !coalesceMetadata(&variation, existing) {
				continue // already matches
			}
			existing.Spec = comp.Spec
			err = c.client.Update(ctx, existing)
			if err != nil {
				return false, fmt.Errorf("updating existing composition: %w", err)
			}

			logger.V(0).Info("updated composition because its variation changed", "compositionName", existing.Name, "compositionNamespace", existing.Namespace)
			return true, nil
		}

		// Update the symphony status before creating to avoid conflicts
		// The next creation will fail if a composition has already been created for this synthesizer ref.
		symph.Status.Synthesizers = append(symph.Status.Synthesizers, apiv1.SynthesizerRef{Name: comp.Name})
		sortSynthesizerRefs(symph.Status.Synthesizers)
		if err := c.client.Status().Update(ctx, symph); err != nil {
			return false, fmt.Errorf("adding synthesizer to status: %w", err)
		}

		err = c.client.Create(ctx, comp)
		if k8serrors.IsForbidden(err) && k8serrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
			logger.V(0).Info("skipping composition creation because the namespace is being terminated", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("creating composition: %w", err)
		}

		logger.V(0).Info("created composition for symphony", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
		return true, nil
	}

	return false, nil
}

func (c *symphonyController) syncStatus(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (bool, error) {
	refs := make([]apiv1.SynthesizerRef, len(comps.Items))
	for i, comp := range comps.Items {
		refs[i] = apiv1.SynthesizerRef{Name: comp.Spec.Synthesizer.Name}
	}
	sortSynthesizerRefs(refs)

	if equality.Semantic.DeepEqual(refs, symph.Status.Synthesizers) {
		return false, nil
	}

	symph.Status.Synthesizers = refs
	if err := c.client.Status().Update(ctx, symph); err != nil {
		return false, fmt.Errorf("syncing status: %w", err)
	}

	logr.FromContextOrDiscard(ctx).V(1).Info("sync'd symphony status with composition index")
	return true, nil
}

// getBindings generates the bindings for a variation given it's symphony.
// Bindings specified by a variation take precedence over the symphony.
func getBindings(symph *apiv1.Symphony, vrn *apiv1.Variation) []apiv1.Binding {
	res := append([]apiv1.Binding(nil), symph.Spec.Bindings...)
	for _, bnd := range vrn.Bindings {
		i := slices.IndexFunc(res, func(b apiv1.Binding) bool { return b.Key == bnd.Key })
		if i >= 0 {
			res[i] = bnd
		} else {
			res = append(res, bnd)
		}
	}
	deduped := []apiv1.Binding{}
	for i, bnd := range res {
		j := slices.IndexFunc(res, func(b apiv1.Binding) bool { return b.Key == bnd.Key })
		if i > j {
			continue // duplicate
		}
		deduped = append(deduped, bnd)
	}
	return deduped
}

func sortSynthesizerRefs(refs []apiv1.SynthesizerRef) {
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
}

func coalesceMetadata(variation *apiv1.Variation, existing *apiv1.Composition) bool {
	var metaChanged bool
	for key, val := range variation.Labels {
		if existing.Labels == nil || existing.Labels[key] != val {
			metaChanged = true
		}
		existing.Labels[key] = val
	}
	for key, val := range variation.Annotations {
		if existing.Annotations == nil || existing.Annotations[key] != val {
			metaChanged = true
		}
		existing.Annotations[key] = val
	}
	return metaChanged
}
