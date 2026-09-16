package backup

import (
	"context"
	"fmt"
	"slices"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (c *backupController) newDeletedCompositionHandler() handler.TypedEventHandler[*apiv1.Composition, reconcile.Request] {
	return &handler.TypedFuncs[*apiv1.Composition, reconcile.Request]{
		DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[*apiv1.Composition], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if !c.enabled {
				return
			}
			if e.DeleteStateUnknown {
				logr.FromContextOrDiscard(ctx).Info("retaining inventory because the deleted composition state is unknown",
					"operation", "inventoryCleanup", "cleanupTrigger", "compositionDeletion",
					"compositionName", e.Object.Name, "compositionNamespace", e.Object.Namespace)
				return
			}
			comp := e.Object.DeepCopy()
			key := client.ObjectKeyFromObject(comp)
			// A queued request cannot GET the deleted object. Retain its routing and
			// owner identity in memory; cleanup must not hold Composition deletion.
			c.deletedCompositionsMu.Lock()
			if c.deletedCompositions == nil {
				c.deletedCompositions = map[types.NamespacedName]*apiv1.Composition{}
			}
			c.deletedCompositions[key] = comp
			c.deletedCompositionsMu.Unlock()
			q.Add(reconcile.Request{NamespacedName: key})
		},
	}
}

func (c *backupController) retireDeletedComposition(ctx context.Context, key types.NamespacedName) (bool, error) {
	c.deletedCompositionsMu.Lock()
	comp := c.deletedCompositions[key]
	c.deletedCompositionsMu.Unlock()
	if comp == nil {
		return false, nil
	}
	ctx = inventoryCleanupContext(ctx, comp, "")
	logger := logr.FromContextOrDiscard(ctx).WithValues("operation", "inventoryCleanup", "cleanupTrigger", "compositionDeletion", "reason", "variationRemoved",
		"synthesisUUID", comp.Status.GetCurrentSynthesisUUID())
	ctx = logr.NewContext(ctx, logger)
	if err := c.deleteRemovedVariationInventory(ctx, comp); err != nil {
		logger.Error(err, "failed to retire inventory for deleted composition; retrying")
		return true, err
	}
	c.deletedCompositionsMu.Lock()
	if c.deletedCompositions[key] == comp {
		delete(c.deletedCompositions, key)
	}
	c.deletedCompositionsMu.Unlock()
	return true, nil
}

func (c *backupController) deleteRemovedVariationInventory(ctx context.Context, comp *apiv1.Composition) error {
	if !c.enabled {
		return nil
	}
	matches, err := c.ownsComposition(ctx, comp)
	if err != nil {
		return err
	}
	logger := logr.FromContextOrDiscard(ctx)
	if !matches {
		logger.V(1).Info("deleted composition is outside the backup resource filter")
		return nil
	}
	if eligible, err := variationInventoryCanBeDeleted(ctx, c.reader, comp); err != nil || !eligible {
		return err
	}
	items, _, err := c.readInventories(ctx, comp)
	if err != nil {
		return err
	}
	retired := 0
	for _, item := range items {
		snapshot, err := decodeInventory(comp, item)
		if err != nil {
			return err
		}
		source, err := snapshot.cleanupComposition()
		if err != nil {
			return err
		}
		if source == nil {
			logger.Info("retaining inventory without cleanup ownership metadata", "inventoryConfigMap", item.Name)
			continue
		}
		matches, err := c.ownsComposition(ctx, source)
		if err != nil {
			return err
		}
		if !matches {
			logger.Info("retaining inventory outside the configured ownership scope", "inventoryConfigMap", item.Name)
			continue
		}
		eligible, err := variationInventoryCanBeDeleted(ctx, c.reader, source)
		if err != nil {
			return err
		}
		if !eligible {
			continue
		}
		if eligible, err := variationInventoryCanBeDeleted(ctx, c.reader, comp); err != nil || !eligible {
			return err
		}
		err = deleteInventorySnapshot(ctx, c.downstream, &item)
		if err != nil {
			return fmt.Errorf("deleting removed variation's inventory ConfigMap %q: %w", item.Name, err)
		}
		retired++
		logger.Info("removed variation inventory cleaned up", "inventoryConfigMap", item.Name)
	}
	logger.Info("inventory retirement completed", "listedSnapshotCount", len(items), "retiredSnapshotCount", retired)
	return nil
}

func variationInventoryCanBeDeleted(ctx context.Context, reader client.Reader, comp *apiv1.Composition) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	owner := metav1.GetControllerOf(comp)
	if owner == nil || owner.Kind != "Symphony" || owner.APIVersion != apiv1.SchemeGroupVersion.String() || owner.UID == "" {
		logger.Info("retaining inventory because composition has no identifiable owning Symphony")
		return false, nil
	}
	if comp.Labels["eno.azure.io/symphony-deleting"] == "true" {
		logger.Info("retaining inventory for Symphony teardown")
		return false, nil
	}
	// Inventory lineage excludes Composition and Symphony UIDs. A replacement
	// or another Composition using the same synthesizer must retain that history.
	continuation := ""
	for {
		comps := &apiv1.CompositionList{}
		if err := reader.List(ctx, comps, client.InNamespace(comp.Namespace), client.Limit(100), client.Continue(continuation)); err != nil {
			return false, fmt.Errorf("checking inventory lineage users: %w", err)
		}
		for _, other := range comps.Items {
			if other.Name == comp.Name || other.Spec.Synthesizer.Name == comp.Spec.Synthesizer.Name {
				logger.Info("retaining inventory because a composition still uses its name or lineage", "observedCompositionName", other.Name, "observedCompositionUID", other.UID)
				return false, nil
			}
		}
		continuation = comps.Continue
		if continuation == "" {
			break
		}
	}
	symph := &apiv1.Symphony{}
	err := reader.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: owner.Name}, symph)
	if apierrors.IsNotFound(err) {
		logger.Info("retaining inventory because owning Symphony is absent", "symphonyName", owner.Name)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading owning Symphony %q: %w", owner.Name, err)
	}
	if symph.UID != owner.UID || symph.DeletionTimestamp != nil {
		logger.Info("retaining inventory because owning Symphony was replaced or is deleting", "symphonyName", owner.Name, "observedSymphonyUID", symph.UID)
		return false, nil
	}
	if slices.ContainsFunc(symph.Spec.Variations, func(v apiv1.Variation) bool {
		return v.Synthesizer.Name == comp.Spec.Synthesizer.Name
	}) {
		logger.Info("retaining inventory because the variation is still desired", "symphonyName", symph.Name)
		return false, nil
	}
	return true, nil
}

func deleteInventorySnapshot(ctx context.Context, downstream client.Client, item *corev1.ConfigMap) error {
	if item.UID == "" || item.ResourceVersion == "" {
		return fmt.Errorf("inventory ConfigMap %q has no UID or resourceVersion for conditional deletion", item.Name)
	}
	return client.IgnoreNotFound(downstream.Delete(ctx, item, &client.Preconditions{UID: &item.UID, ResourceVersion: &item.ResourceVersion}))
}

func inventoryCleanupContext(ctx context.Context, comp *apiv1.Composition, inventoryName string) context.Context {
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace,
		"compositionUID", comp.UID, "synthesizerName", comp.Spec.Synthesizer.Name, "lineage", inventoryLineage(comp))
	if inventoryName != "" {
		logger = logger.WithValues("inventoryConfigMap", inventoryName)
	}
	if owner := metav1.GetControllerOf(comp); owner != nil {
		logger = logger.WithValues("symphonyName", owner.Name, "symphonyUID", owner.UID)
	}
	return logr.NewContext(ctx, logger)
}
