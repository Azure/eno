package tombstonerecovery

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	inventoryGCInterval = 3 * time.Minute
	inventoryGCJitter   = 0.5 // Cycles start every 3 to 4.5 minutes.
)

// inventoryGarbageCollector periodically deletes stale inventory Secrets while the reconciler is running.
type inventoryGarbageCollector struct {
	reader     client.Reader
	downstream client.Client
	namespace  string
	logger     logr.Logger
}

func (g *inventoryGarbageCollector) NeedLeaderElection() bool {
	return true
}

func (g *inventoryGarbageCollector) Start(ctx context.Context) error {
	ctx = logr.NewContext(ctx, g.logger)
	wait.JitterUntilWithContext(ctx, g.runCycle, inventoryGCInterval, inventoryGCJitter, true)
	return nil
}

// runCycle logs failures and panics so the loop keeps running; the next cycle starts fresh.
func (g *inventoryGarbageCollector) runCycle(ctx context.Context) {
	logger := logr.FromContextOrDiscard(ctx)
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Errorf("panic: %v", r), "inventory garbage collection cycle panicked", "stack", string(debug.Stack()))
		}
	}()
	if err := g.deleteStaleInventory(ctx); err != nil {
		logger.Error(err, "skipping inventory garbage collection cycle")
	}
}

// deleteStaleInventory runs one cycle. Compositions are listed before Symphonies so a Composition missing from the list
// was already gone before any Symphony deletion that the later read could observe.
func (g *inventoryGarbageCollector) deleteStaleInventory(ctx context.Context) error {
	logger := logr.FromContextOrDiscard(ctx)
	secrets := &corev1.SecretList{}
	if err := g.downstream.List(ctx, secrets, client.InNamespace(inventoryNamespace), client.HasLabels{inventoryLineageLabel}); err != nil {
		return fmt.Errorf("listing downstream inventory Secrets: %w", err)
	}
	comps := &apiv1.CompositionList{}
	if err := g.reader.List(ctx, comps, client.InNamespace(g.namespace)); err != nil {
		return fmt.Errorf("listing Compositions: %w", err)
	}
	symphonies := &apiv1.SymphonyList{}
	if err := g.reader.List(ctx, symphonies, client.InNamespace(g.namespace)); err != nil {
		return fmt.Errorf("listing Symphonies: %w", err)
	}
	// Eno expects exactly one active Symphony per active cluster cluster; otherwise keep all inventory.
	switch {
	case len(symphonies.Items) == 0:
		logger.Info("no symphony found; keeping inventory", "scanned", len(secrets.Items))
		return nil
	case len(symphonies.Items) > 1:
		logger.Error(nil, "found multiple symphonies, expected exactly one; keeping inventory",
			"symphonyCount", len(symphonies.Items), "scanned", len(secrets.Items))
		return nil
	case symphonies.Items[0].DeletionTimestamp != nil:
		logger.Info("symphony is deleting; keeping inventory",
			"symphonyName", symphonies.Items[0].Name, "scanned", len(secrets.Items))
		return nil
	}

	stale := findStaleInventory(secrets.Items, comps.Items, &symphonies.Items[0])
	deleted, failed := 0, 0
	for i := range stale {
		item := &stale[i]
		itemLogger := logger.WithValues("secretName", item.Name, "lineage", item.Labels[inventoryLineageLabel],
			"synthesisUUID", item.Labels[inventorySynthesisUUIDLabel])
		if err := deleteInventorySecret(ctx, g.downstream, item); err != nil {
			itemLogger.Error(err, "failed to delete stale inventory Secret")
			failed++
			continue
		}
		itemLogger.Info("deleted stale inventory Secret")
		deleted++
	}
	logger.Info("inventory garbage collection cycle finished", "scanned", len(secrets.Items), "deleted", deleted, "failed", failed)
	return nil
}

// findStaleInventory returns the inventory Secrets that are safe to delete.
func findStaleInventory(secrets []corev1.Secret, comps []apiv1.Composition, symph *apiv1.Symphony) []corev1.Secret {
	stale := findOrphanedLineageInventory(secrets, comps, symph)
	stale = append(stale, findInventoryFromOldSyntheses(secrets, comps)...)
	sort.Slice(stale, func(i, j int) bool {
		return stale[i].Name < stale[j].Name
	})
	return stale
}

// findOrphanedLineageInventory returns every Secret whose lineage matches no Composition and no Symphony variation.
// A variation without a Composition yet is being (re)created, e.g. after AKS stop/start, and needs its inventory for recovery.
func findOrphanedLineageInventory(secrets []corev1.Secret, comps []apiv1.Composition, symph *apiv1.Symphony) []corev1.Secret {
	liveLineages := make(map[string]bool, len(comps)+len(symph.Spec.Variations))
	for i := range comps {
		liveLineages[inventoryLineage(&comps[i])] = true
	}
	for _, variation := range symph.Spec.Variations {
		liveLineages[inventoryLineageFor(symph.Namespace, variation.Synthesizer.Name)] = true
	}
	var orphaned []corev1.Secret
	for _, secret := range secrets {
		if !liveLineages[secret.Labels[inventoryLineageLabel]] {
			orphaned = append(orphaned, secret)
		}
	}
	return orphaned
}

// findInventoryFromOldSyntheses returns each existing Composition's Secrets from older syntheses,
// once its current synthesis has a complete snapshot.
func findInventoryFromOldSyntheses(secrets []corev1.Secret, comps []apiv1.Composition) []corev1.Secret {
	secretsByLineage := groupInventoryByLineage(secrets)
	var old []corev1.Secret
	for i := range comps {
		comp := &comps[i]
		// Event-driven cleanup owns a deleting Composition's inventory.
		if comp.DeletionTimestamp != nil || comp.Status.CurrentSynthesis == nil {
			continue
		}
		items := secretsByLineage[inventoryLineage(comp)]
		old = append(old, oldSnapshotsInLineage(items, comp.Status.CurrentSynthesis.UUID)...)
	}
	return old
}

// groupInventoryByLineage groups inventory Secrets by their lineage label.
func groupInventoryByLineage(secrets []corev1.Secret) map[string][]corev1.Secret {
	secretsByLineage := map[string][]corev1.Secret{}
	for _, secret := range secrets {
		lineage := secret.Labels[inventoryLineageLabel]
		secretsByLineage[lineage] = append(secretsByLineage[lineage], secret)
	}
	return secretsByLineage
}

// oldSnapshotsInLineage returns one lineage's Secrets from older syntheses, only when the current synthesis has a complete snapshot.
func oldSnapshotsInLineage(items []corev1.Secret, currentUUID string) []corev1.Secret {
	var current, old []corev1.Secret
	for _, item := range items {
		if item.Labels[inventorySynthesisUUIDLabel] == currentUUID {
			current = append(current, item)
		} else {
			old = append(old, item)
		}
	}
	if !isCompleteSnapshot(current) {
		return nil
	}
	return old
}
