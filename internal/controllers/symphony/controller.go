package symphony

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

func NewController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Symphony{}).
		Owns(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "symphonyController")).
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

	if controllerutil.AddFinalizer(symph, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, symph)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	existing := &apiv1.CompositionList{}
	err = c.client.List(ctx, existing, client.InNamespace(symph.Namespace), client.MatchingFields{
		manager.IdxCompositionsBySymphony: symph.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing existing compositions: %w", err)
	}

	modified, err := c.reconcileReverse(ctx, symph, existing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if modified {
		return ctrl.Result{}, nil
	}

	// Remove finalizer when no compositions remain
	if symph.DeletionTimestamp != nil {
		if len(existing.Items) > 0 || !controllerutil.RemoveFinalizer(symph, "eno.azure.io/cleanup") {
			return ctrl.Result{}, nil
		}
		err = c.client.Update(ctx, symph)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	modified, err = c.reconcileForward(ctx, symph, existing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if modified {
		return ctrl.Result{}, nil
	}

	err = c.syncStatus(ctx, symph, existing)
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (c *symphonyController) reconcileReverse(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (bool, error) {
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

		hasVariation := slices.ContainsFunc(symph.Spec.Variations, func(vrn apiv1.Variation) bool {
			return vrn.Synthesizer.Name == comp.Spec.Synthesizer.Name
		})
		if (hasVariation && symph.DeletionTimestamp == nil) || comp.DeletionTimestamp != nil {
			continue
		}

		err := c.client.Delete(ctx, &comp)
		if err != nil {
			return false, fmt.Errorf("cleaning up composition: %w", err)
		}

		logger.V(0).Info("deleted composition because its variation was removed from the symphony", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
		return true, nil
	}

	// Delete any duplicates we may have created in the past - leave the oldest one
	for _, comps := range existingBySynthName {
		if len(comps) < 2 {
			continue
		}

		sort.Slice(comps, func(i, j int) bool { return comps[i].CreationTimestamp.Before(&comps[j].CreationTimestamp) })

		err := c.client.Delete(ctx, comps[0])
		if err != nil {
			return false, fmt.Errorf("deleting duplicate composition: %w", err)
		}

		logger.V(0).Info("deleted composition because it's a duplicate", "compositionName", comps[0].Name, "compositionNamespace", comps[0].Namespace)
		return true, nil
	}

	return false, nil
}

func (c *symphonyController) reconcileForward(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (modified bool, err error) {
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
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)

		// Compose missing variations
		idx := slices.IndexFunc(comps.Items, func(existing apiv1.Composition) bool {
			return existing.Spec.Synthesizer.Name == variation.Synthesizer.Name
		})
		if idx == -1 {
			err := c.client.List(ctx, comps, client.InNamespace(symph.Namespace))
			if err != nil {
				return false, fmt.Errorf("listing existing compositions without cache: %w", err)
			}
			for _, cur := range comps.Items {
				if cur.Spec.Synthesizer.Name == variation.Synthesizer.Name {
					return false, fmt.Errorf("stale cache - composition already exists")
				}
			}

			err = c.client.Create(ctx, comp)
			if k8serrors.IsForbidden(err) && k8serrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
				logger.V(0).Info("skipping composition creation because the namespace is being terminated")
				return false, nil
			}
			if err != nil {
				return false, fmt.Errorf("creating composition: %w", err)
			}
			logger.V(0).Info("created composition for symphony")
			return true, nil
		}

		// Diff and update if needed
		existing := comps.Items[idx]
		if equality.Semantic.DeepEqual(comp.Spec, existing.Spec) && !coalesceMetadata(&variation, &existing) {
			continue // already matches
		}
		existing.Spec = comp.Spec
		err = c.client.Update(ctx, &existing)
		if err != nil {
			return false, fmt.Errorf("updating existing composition: %w", err)
		}
		logger.V(0).Info("updated composition because its variation changed")
		return true, nil
	}

	return false, nil
}

func (c *symphonyController) syncStatus(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) error {
	logger := logr.FromContextOrDiscard(ctx)

	newStatus := c.buildStatus(symph, comps)
	if equality.Semantic.DeepEqual(newStatus, symph.Status) {
		return nil
	}

	copy := symph.DeepCopy()
	copy.Status = newStatus
	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(symph)); err != nil {
		return fmt.Errorf("syncing status: %w", err)
	}

	logger.V(1).Info("sync'd symphony status", "ready", newStatus.Ready != nil, "reconciled", newStatus.Reconciled != nil, "synthesized", newStatus.Synthesized != nil)
	return nil
}

func (c *symphonyController) buildStatus(symph *apiv1.Symphony, comps *apiv1.CompositionList) apiv1.SymphonyStatus {
	newStatus := apiv1.SymphonyStatus{
		ObservedGeneration: symph.Generation,
	}
	synthMap := map[string]struct{}{}
	for _, comp := range comps.Items {
		synthMap[comp.Spec.Synthesizer.Name] = struct{}{}

		syn := comp.Status.CurrentSynthesis
		if syn == nil {
			continue
		}

		if newStatus.Ready.Before(syn.Ready) || newStatus.Ready == nil {
			newStatus.Ready = syn.Ready
		}
		if newStatus.Reconciled.Before(syn.Reconciled) || newStatus.Reconciled == nil {
			newStatus.Reconciled = syn.Reconciled
		}
		if newStatus.Synthesized.Before(syn.Synthesized) || newStatus.Synthesized == nil {
			newStatus.Synthesized = syn.Synthesized
		}
	}

	// Status should be nil for any states that haven't been reached by all compositions
	for _, comp := range comps.Items {
		syn := comp.Status.CurrentSynthesis
		synInvalid := syn == nil || syn.ObservedCompositionGeneration != comp.Generation || comp.DeletionTimestamp != nil

		if synInvalid || syn.Ready == nil {
			newStatus.Ready = nil
		}
		if synInvalid || syn.Reconciled == nil {
			newStatus.Reconciled = nil
		}
		if synInvalid || syn.Synthesized == nil {
			newStatus.Synthesized = nil
		}
	}

	return newStatus
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

func coalesceMetadata(variation *apiv1.Variation, existing *apiv1.Composition) bool {
	var metaChanged bool

	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for key, val := range variation.Labels {
		if existing.Labels[key] != val {
			metaChanged = true
		}
		existing.Labels[key] = val
	}

	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for key, val := range variation.Annotations {
		if existing.Annotations[key] != val {
			metaChanged = true
		}
		existing.Annotations[key] = val
	}
	return metaChanged
}
