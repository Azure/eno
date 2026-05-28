package symphony

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type symphonyController struct {
	client        client.Client
	noCacheClient client.Reader
}
type symphonyConditions struct {
	name       string
	appliedMsg string // copied from compositions' ResoruceApplied condition. "" if synthInvalid or unset
	readyMsg   string // copied from compositions' ResourceReady condition. "" if synthInvalid or unset
	notApplied bool
	notReady   bool
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

	// NEW: Build map of synthesizer name -> existing composition for dependency resolution
	compBySynth := make(map[string]*apiv1.Composition, len(comps.Items))
	for i := range comps.Items {
		compBySynth[comps.Items[i].Spec.Synthesizer.Name] = &comps.Items[i]
	}

	// Sort variations so dependencies come first; detect cycles as a byproduct
	sortedVariations, cyclicSynths := topoSortVariations(symph.Spec.Variations)
	for synthName := range cyclicSynths {
		logger.Error(fmt.Errorf("circular dependency detected in variation"),
			"skipping variation",
			"synthesizerName", synthName)
	}

	// Sync forward (create/update) — process ALL variations in dependency order
	for _, variation := range sortedVariations {

		comp := &apiv1.Composition{}
		comp.Namespace = symph.Namespace
		comp.GenerateName = variation.Synthesizer.Name + "-"
		comp.Spec.Bindings = getBindings(symph, &variation)
		comp.Spec.Synthesizer = variation.Synthesizer
		comp.Spec.SynthesisEnv = getSynthesisEnv(symph, &variation)
		comp.Labels = variation.Labels
		comp.Annotations = variation.Annotations

		// Resolve variation dependencies to composition dependencies. If the dependent composition does not exist yet
		// DO NOT CREATE the current composition as it might lead to race condition and ordering not being respected
		var validDeps []apiv1.VariationDependency
		for _, dep := range variation.DependsOn {
			if dep.Synthesizer == "" {
				logger.Error(fmt.Errorf("No Variation Dependency Synthesizer"),
					"Error: variation dependency has no synthesizer set, dependency will be ignored",
					"synthesizerName", variation.Synthesizer.Name)
				continue
			}
			validDeps = append(validDeps, dep)
		}
		deps, allresolved := resolveVariationDeps(validDeps, compBySynth)

		comp.Spec.DependsOn = deps
		idx := slices.IndexFunc(comps.Items, func(existing apiv1.Composition) bool {
			return existing.Spec.Synthesizer.Name == variation.Synthesizer.Name
		})

		// Safety fallback: with topological sort this should not trigger since
		// dependencies are processed first, but guards against edge cases like
		// a failed Create earlier in the loop.
		if !allresolved {
			if idx == -1 {
				logger.Error(fmt.Errorf("not all synthesizer-based dependencies resolved yet"),
					"skipping composition creation",
					"synthesizerName", variation.Synthesizer.Name)
			} else {
				logger.Error(fmt.Errorf("not all synthesizer-based dependencies resolved yet"),
					"skipping composition update",
					"synthesizerName", variation.Synthesizer.Name, "compositionName", comps.Items[idx].Name)
			}
			continue
		}

		err := controllerutil.SetControllerReference(symph, comp, c.client.Scheme())
		if err != nil {
			logger.Error(err, "failed to set controller reference", "synthesizerName", variation.Synthesizer.Name)
			return false, fmt.Errorf("setting composition's controller: %w", err)
		}
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace,
			"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())

		if idx == -1 {
			// Assumption to ensure idx == -1 works correctly.
			// noCacheClient.List returns all compositions in the namespace, which under the below assumptions is correct.
			// Need to revisit if any of the below assumptions change.
			// 1. One symphony per namespace
			// 2. No standalone compositions in the namespace
			// 3. No cross-symphony dependency references
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
			// Add to compBySynth so later iterations can resolve deps on this composition
			compBySynth[variation.Synthesizer.Name] = comp
			modified = true
			continue
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
		modified = true
	}

	logger.Info("reconcile forward complete")
	return modified, nil
}

func (c *symphonyController) syncStatus(ctx context.Context, symph *apiv1.Symphony, comps *apiv1.CompositionList) error {
	logger := logr.FromContextOrDiscard(ctx)

	newStatus, blockers := c.buildStatus(symph, comps)

	cond := metav1.Condition{
		Type:               apiv1.ConditionSymphonyReady,
		ObservedGeneration: symph.Generation,
	}
	if len(blockers) == 0 {
		cond.Status = metav1.ConditionTrue
		cond.Reason = apiv1.AllCompositionReadyReason
	} else {
		cond.Status = metav1.ConditionFalse
		cond.Reason = apiv1.NotAllCompositionReadyReason
		cond.Message = formatSymphonyMessage(blockers)
	}
	// Seed Conditions from the existing status so meta.SetStatusCondition
	// preserves LastTransitionTime when nothing has actually flipped. This is
	// what makes the DeepEqual short-circuit below idempotent across reconciles.
	newStatus.Conditions = symph.Status.DeepCopy().Conditions
	meta.SetStatusCondition(&newStatus.Conditions, cond)

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

func (c *symphonyController) buildStatus(symph *apiv1.Symphony, comps *apiv1.CompositionList) (apiv1.SymphonyStatus, []symphonyConditions) {
	newStatus := apiv1.SymphonyStatus{
		ObservedGeneration: symph.Generation,
	}
	var conditionStatus []symphonyConditions
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
		synthInvalid := syn == nil || syn.ObservedCompositionGeneration != comp.Generation || comp.DeletionTimestamp != nil
		if synthInvalid || syn.Ready == nil {
			newStatus.Ready = nil
		}
		if synthInvalid || syn.Reconciled == nil {
			newStatus.Reconciled = nil
		}
		if synthInvalid || syn.Synthesized == nil {
			newStatus.Synthesized = nil
		}

		blockingCondition := symphonyConditions{name: comp.Name}
		if synthInvalid {
			blockingCondition.notApplied = true
			blockingCondition.notReady = true
		} else {
			if condition := meta.FindStatusCondition(syn.Conditions, apiv1.ConditionResourcesApplied); condition != nil && condition.Status != metav1.ConditionTrue {
				blockingCondition.notApplied = true
				blockingCondition.appliedMsg = condition.Message
			}
			if condition := meta.FindStatusCondition(syn.Conditions, apiv1.ConditionResourcesReady); condition != nil && condition.Status != metav1.ConditionTrue {
				blockingCondition.notReady = true
				blockingCondition.readyMsg = condition.Message
			}
		}

		if blockingCondition.notApplied || blockingCondition.notReady {
			conditionStatus = append(conditionStatus, blockingCondition)
		}
	}

	// Sort by composition name for write idempotency. Resource ordering within each section is inherited from the child composition's condition message
	sort.Slice(conditionStatus, func(i, j int) bool { return conditionStatus[i].name < conditionStatus[j].name })

	return newStatus, conditionStatus
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

func resolveVariationDeps(varDeps []apiv1.VariationDependency, compBySynth map[string]*apiv1.Composition) ([]apiv1.CompositionDependency, bool) {
	if len(varDeps) == 0 {
		return nil, true
	}

	allResolved := true
	var resolved []apiv1.CompositionDependency
	for _, dep := range varDeps {
		target, ok := compBySynth[dep.Synthesizer]
		if !ok {
			allResolved = false
			continue // composition does not exist yet - will resolve on next reconcile
		}
		resolved = append(resolved, apiv1.CompositionDependency{
			Name:      target.Name,
			Namespace: target.Namespace,
		})

	}
	return resolved, allResolved
}

func formatSymphonyMessage(symphonyConditions []symphonyConditions) string {
	var notAppliedComps, notReadyComps []string
	for _, cond := range symphonyConditions {
		if cond.notApplied {
			notAppliedComps = append(notAppliedComps, fmt.Sprintf("%s [%s]", cond.name, cond.appliedMsg))
		}
		if cond.notReady {
			notReadyComps = append(notReadyComps, fmt.Sprintf("%s [%s]", cond.name, cond.readyMsg))
		}
	}
	var lines []string
	if len(notAppliedComps) > 0 {
		lines = append(lines, "NotApplied: "+strings.Join(notAppliedComps, ", "))
	}
	if len(notReadyComps) > 0 {
		lines = append(lines, "NotReady: "+strings.Join(notReadyComps, ", "))
	}
	return strings.Join(lines, "\n")
}
