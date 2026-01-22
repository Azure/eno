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
	"k8s.io/apimachinery/pkg/api/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type symphonyController struct {
	client        client.Client
	noCacheClient client.Reader
}

func NewController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Symphony{}).
		Owns(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "symphonyController")).
		Complete(&symphonyController{
			client:        mgr.GetClient(),
			noCacheClient: mgr.GetAPIReader(),
		})
}

func (c *symphonyController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	symph := &apiv1.Symphony{}
	err := c.client.Get(ctx, req.NamespacedName, symph)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get symphony")
		return ctrl.Result{}, err
	}
	logger = logger.WithValues("symphonyName", symph.Name, "symphonyNamespace", symph.Namespace, "symphonyGeneration", symph.Generation,
		"operationID", symph.GetAzureOperationID(), "operationOrigin", symph.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)

	logger.Info("reconciling symphony", "variationCount", len(symph.Spec.Variations), "deleting", symph.DeletionTimestamp != nil)
	if controllerutil.AddFinalizer(symph, "eno.azure.io/cleanup") {
		err := c.client.Update(ctx, symph)
		if err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("added cleanup finalizer to symphony")
		return ctrl.Result{}, nil
	}

	existing := &apiv1.CompositionList{}
	err = c.client.List(ctx, existing, client.InNamespace(symph.Namespace), client.MatchingFields{
		manager.IdxCompositionsBySymphony: symph.Name,
	})
	if err != nil {
		logger.Error(err, "failed to list existing compositions")
		return ctrl.Result{}, err
	}
	logger.Info("listed existing compositions", "compositionCount", len(existing.Items))

	modified, err := c.reconcileReverse(ctx, symph, existing)
	if err != nil {
		logger.Error(err, "failed to reconcile reverse")
		return ctrl.Result{}, err
	}
	if modified {
		logger.Info("reconcile reverse modified resources, requeueing")
		return ctrl.Result{}, nil
	}

	// Remove finalizer when no compositions remain
	if symph.DeletionTimestamp != nil {
		logger.Info("symphony is being deleted", "remainingCompositions", len(existing.Items))
		if len(existing.Items) > 0 || !controllerutil.RemoveFinalizer(symph, "eno.azure.io/cleanup") {
			logger.Info("finalizer already removed from symphony")
			return ctrl.Result{}, nil
		}
		logger.Info("removing cleanup finalizer from symphony")
		err = c.client.Update(ctx, symph)
		if err != nil {
			logger.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("removed cleanup finalizer from symphony")
		return ctrl.Result{}, nil
	}

	modified, err = c.reconcileForward(ctx, symph, existing)
	if err != nil {
		logger.Error(err, "failed to reconcile forward")
		return ctrl.Result{}, err
	}
	if modified {
		logger.Info("reconcile forward modified resources, requeueing")
		return ctrl.Result{}, nil
	}

	logger.Info("syncing symphony status")
	err = c.syncStatus(ctx, symph, existing)
	if err != nil {
		logger.Error(err, "failed to sync status")
		return ctrl.Result{}, err
	}

	logger.Info("symphony reconciliation complete")
	return ctrl.Result{}, nil
}

func (c *symphonyController) reconcileReverse(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	const deletionLabelKey = "eno.azure.io/symphony-deleting"

	expectedSynths := map[string]struct{}{}
	for _, variation := range symph.Spec.Variations {
		expectedSynths[variation.Synthesizer.Name] = struct{}{}
	}

	logger.Info("reconciling reverse - checking for compositions to delete", "variationCount", len(symph.Spec.Variations), "existingCompositionCount", len(comps.Items), "symphonyDeleting", symph.DeletionTimestamp != nil)
	// Delete compositions when their synth has been removed from the symphony
	existingBySynthName := map[string][]*apiv1.Composition{}
	for _, comp := range comps.Items {
		comp := comp
		existingBySynthName[comp.Spec.Synthesizer.Name] = append(existingBySynthName[comp.Spec.Synthesizer.Name], &comp)

		hasVariation := slices.ContainsFunc(symph.Spec.Variations, func(vrn apiv1.Variation) bool {
			return vrn.Synthesizer.Name == comp.Spec.Synthesizer.Name
		})
		shouldExist := hasVariation && symph.DeletionTimestamp == nil
		labelInSync := symph.DeletionTimestamp == nil || (comp.Labels != nil && comp.Labels[deletionLabelKey] == "true")
		alreadyDeleted := comp.DeletionTimestamp != nil
		if shouldExist || (alreadyDeleted && labelInSync) {
			continue
		}

		// Signal that the deletion was caused by symphony deletion, not because the variation was removed
		if !labelInSync {
			logger.Info("labeling composition before deletion", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "reason", "symphony deletion")
			if comp.Labels == nil {
				comp.Labels = map[string]string{}
			}
			comp.Labels[deletionLabelKey] = "true"

			err := c.client.Update(ctx, &comp)
			if err != nil {
				logger.Error(err, "failed to update composition labels before deletion", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
				return false, fmt.Errorf("updating composition labels: %w", err)
			}
			logger.Info("labeled composition before deleting it", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
			return true, nil
		}

		err := c.client.Delete(ctx, &comp)
		if err != nil {
			logger.Error(err, "failed to delete composition", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
			return false, fmt.Errorf("cleaning up composition: %w", err)
		}

		logger.Info("deleted composition", "compositionName", comp.Name, "compositionNamespace", comp.Namespace)
		return true, nil
	}

	// Delete any duplicates we may have created in the past - leave the oldest one
	for synthName, comps := range existingBySynthName {
		if len(comps) < 2 {
			continue
		}

		logger.Info("found duplicate compositions for synthesizer", "synthesizerName", synthName, "duplicateCount", len(comps))
		sort.Slice(comps, func(i, j int) bool { return comps[i].CreationTimestamp.Before(&comps[j].CreationTimestamp) })

		err := c.client.Delete(ctx, comps[0])
		if err != nil {
			logger.Error(err, "failed to delete duplicate composition", "compositionName", comps[0].Name, "compositionNamespace", comps[0].Namespace)
			return false, fmt.Errorf("deleting duplicate composition: %w", err)
		}

		logger.Info("deleted composition because it's a duplicate", "compositionName", comps[0].Name, "compositionNamespace", comps[0].Namespace)
		return true, nil
	}

	logger.Info("reconcile reverse complete")
	return false, nil
}

func (c *symphonyController) reconcileForward(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) (modified bool, err error) {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("reconciling forward - syncing compositions with variations", "variationCount", len(symph.Spec.Variations), "existingCompositionCount", len(comps.Items))

	// Prune any annotations that are currently set to empty strings.
	// This happens before populating other values to enable workflows where an annotation can only be set on (at most) one composition.
	for _, variation := range symph.Spec.Variations {
		idx := slices.IndexFunc(comps.Items, func(existing apiv1.Composition) bool {
			return existing.Spec.Synthesizer.Name == variation.Synthesizer.Name
		})
		if idx == -1 {
			logger.Info("variation has no existing composition, skipping annotation pruning", "synthesizerName", variation.Synthesizer.Name)
			continue // composition doesn't exist yet, nothing to remove
		}

		existing := &comps.Items[idx]
		if pruneAnnotations(&variation, existing) {
			logger.Info("pruning empty annotations from composition", "compositionName", existing.Name, "compositionNamespace", existing.Namespace, "synthesizerName", variation.Synthesizer.Name)
			err := c.client.Update(ctx, existing)
			if err != nil {
				logger.Error(err, "failed to prune annotations from composition", "compositionName", existing.Name, "compositionNamespace", existing.Namespace)
				return false, fmt.Errorf("pruning annotations: %w", err)
			}
			logger.Info("pruned annotations from composition", "compositionName", existing.Name, "compositionNamespace", existing.Namespace)

			return true, nil
		}
	}

	// Sync forward (create/update)
	for _, variation := range symph.Spec.Variations {
		comp := &apiv1.Composition{}
		comp.Namespace = symph.Namespace
		comp.GenerateName = variation.Synthesizer.Name + "-"
		comp.Spec.Bindings = getBindings(symph, &variation)
		comp.Spec.Synthesizer = variation.Synthesizer
		comp.Spec.SynthesisEnv = getSynthesisEnv(symph, &variation)
		comp.Labels = variation.Labels
		comp.Annotations = variation.Annotations
		err := controllerutil.SetControllerReference(symph, comp, c.client.Scheme())
		if err != nil {
			logger.Error(err, "failed to set controller reference", "synthesizerName", variation.Synthesizer.Name)
			return false, fmt.Errorf("setting composition's controller: %w", err)
		}
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace,
			"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())

		// Compose missing variations
		idx := slices.IndexFunc(comps.Items, func(existing apiv1.Composition) bool {
			return existing.Spec.Synthesizer.Name == variation.Synthesizer.Name
		})
		if idx == -1 {
			err := c.noCacheClient.List(ctx, comps, client.InNamespace(symph.Namespace))
			if err != nil {
				logger.Error(err, "failed to list compositions without cache", "synthesizerName", variation.Synthesizer.Name)
				return false, fmt.Errorf("listing existing compositions without cache: %w", err)
			}
			for _, cur := range comps.Items {
				owner := metav1.GetControllerOf(&cur)
				if owner != nil && owner.UID == symph.UID && cur.Spec.Synthesizer.Name == variation.Synthesizer.Name {
					logger.Error(nil, "stale cache detected - composition already exists", "compositionName", cur.Name, "compositionNamespace", cur.Namespace, "synthesizerName", variation.Synthesizer.Name)
					return false, fmt.Errorf("stale cache - composition already exists")
				}
			}

			logger.Info("creating new composition for variation", "synthesizerName", variation.Synthesizer.Name, "generateName", comp.GenerateName)
			err = c.client.Create(ctx, comp)
			if k8serrors.IsForbidden(err) && k8serrors.HasStatusCause(err, corev1.NamespaceTerminatingCause) {
				logger.Info("skipping composition creation because the namespace is being terminated")
				return false, nil
			}
			if err != nil {
				logger.Error(err, "failed to create composition", "synthesizerName", variation.Synthesizer.Name)
				return false, fmt.Errorf("creating composition: %w", err)
			}
			logger.Info("created composition for symphony", "compositionName", comp.Name, "synthesizerName", variation.Synthesizer.Name)
			return true, nil
		}

		// Diff and update if needed
		existing := comps.Items[idx]
		if equality.Semantic.DeepEqual(comp.Spec, existing.Spec) && !coalesceMetadata(&variation, &existing) {
			continue // already matches
		}
		logger.Info("updating composition to match variation", "compositionName", existing.Name, "compositionNamespace", existing.Namespace, "synthesizerName", variation.Synthesizer.Name)
		existing.Spec = comp.Spec
		err = c.client.Update(ctx, &existing)
		if err != nil {
			logger.Error(err, "failed to update composition", "compositionName", existing.Name, "compositionNamespace", existing.Namespace, "synthesizerName", variation.Synthesizer.Name)
			return false, fmt.Errorf("updating existing composition: %w", err)
		}
		logger.Info("updated composition because its variation changed")
		return true, nil
	}

	logger.Info("reconcile forward complete")
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

	optionalSynths := make(map[string]struct{})
	for _, variation := range symph.Spec.Variations {
		if variation.Optional {
			optionalSynths[variation.Synthesizer.Name] = struct{}{}
		}
	}

	for _, comp := range comps.Items {
		synthMap[comp.Spec.Synthesizer.Name] = struct{}{}

		if _, ok := optionalSynths[comp.Spec.Synthesizer.Name]; ok {
			continue
		}

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
		if _, ok := optionalSynths[comp.Spec.Synthesizer.Name]; ok {
			continue
		}

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

func getSynthesisEnv(symph *apiv1.Symphony, vrn *apiv1.Variation) []apiv1.EnvVar {
	res := append([]apiv1.EnvVar(nil), vrn.SynthesisEnv...)
	for _, evar := range symph.Spec.SynthesisEnv {
		i := slices.IndexFunc(res, func(e apiv1.EnvVar) bool {
			return evar.Name == e.Name
		})
		// Only use symhony var if the variation didn't specify it.
		if i == -1 {
			res = append(res, evar)
		}
	}
	return res
}

func coalesceMetadata(variation *apiv1.Variation, existing *apiv1.Composition) bool {
	var metaChanged bool

	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for key, val := range variation.Labels {
		if val == "" {
			if _, exists := existing.Labels[key]; exists {
				metaChanged = true
				delete(existing.Labels, key)
			}
			continue
		}
		if existing.Labels[key] != val {
			metaChanged = true
		}
		existing.Labels[key] = val
	}

	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	for key, val := range variation.Annotations {
		if val == "" {
			// Skip empty string annotations
			// They've already been removed from the composition by pruneAnnotations
			continue
		}
		if existing.Annotations[key] != val {
			metaChanged = true
			existing.Annotations[key] = val
		}
	}
	return metaChanged
}

func pruneAnnotations(variation *apiv1.Variation, existing *apiv1.Composition) bool {
	if existing.Annotations == nil {
		return false
	}

	var changed bool
	for key, val := range variation.Annotations {
		if val == "" {
			if _, exists := existing.Annotations[key]; exists {
				changed = true
				delete(existing.Annotations, key)
			}
		}
	}
	return changed
}
