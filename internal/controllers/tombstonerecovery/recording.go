package tombstonerecovery

import (
	"context"
	"fmt"
	"reflect"
	"sort"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (c *tombstoneRecoveryController) recordInventory(ctx context.Context, comp *apiv1.Composition) error {
	items, err := c.readInventories(ctx, comp)
	if err != nil {
		return err
	}
	if isInventoryAlreadyRecorded(items, comp.Status.CurrentSynthesis.UUID) {
		return nil
	}
	if _, err := c.getCurrentComposition(ctx, comp, true); err != nil {
		return err
	}
	slices, err := c.loadCurrentSynthesisResourceSlices(ctx, comp)
	if err != nil {
		return err
	}
	secrets, err := makeInventory(comp, slices)
	if err != nil {
		return fmt.Errorf("building inventory: %w", err)
	}
	for i := range secrets {
		if err := c.writeInventoryChunk(ctx, comp, &secrets[i]); err != nil {
			return err
		}
	}
	if err := c.deleteOldInventorySnapshots(ctx, comp, secrets[0].Labels[inventorySynthesisUUIDLabel]); err != nil {
		return err
	}
	logr.FromContextOrDiscard(ctx).V(1).Info("inventory snapshot persisted", "secretCount", len(secrets))
	return nil
}

func (c *tombstoneRecoveryController) writeInventoryChunk(ctx context.Context, comp *apiv1.Composition, intended *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := c.downstream.Get(ctx, client.ObjectKeyFromObject(intended), existing)
	if apierrors.IsNotFound(err) {
		if err := c.downstream.Create(ctx, intended); err != nil {
			return fmt.Errorf("creating inventory Secret %q: %w", intended.Name, err)
		}
		logr.FromContextOrDiscard(ctx).Info("created inventory chunk", "secretName", intended.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading inventory Secret %q: %w", intended.Name, err)
	}
	if inventorySecretMatches(existing, intended) {
		return nil
	}
	return fmt.Errorf("inventory Secret %q has different contents or metadata for synthesis UUID %q",
		existing.Name, intended.Labels[inventorySynthesisUUIDLabel])
}

// isInventoryAlreadyRecorded reports whether the only inventory is a complete one for the current synthesis.
func isInventoryAlreadyRecorded(items []corev1.Secret, currentUUID string) bool {
	if len(items) == 0 {
		return false
	}
	for i := range items {
		if items[i].Labels[inventorySynthesisUUIDLabel] != currentUUID {
			return false
		}
	}
	_, complete := orderInventoryChunks(items)
	return complete
}

func inventorySecretMatches(existing, intended *corev1.Secret) bool {
	return existing.Type == intended.Type &&
		reflect.DeepEqual(existing.Data, intended.Data) &&
		reflect.DeepEqual(existing.Labels, intended.Labels) &&
		reflect.DeepEqual(existing.Annotations, intended.Annotations)
}

func (c *tombstoneRecoveryController) deleteOldInventorySnapshots(ctx context.Context, comp *apiv1.Composition, currentUUID string) error {
	items, err := c.readInventories(ctx, comp)
	if err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Name < items[j].Name
	})
	for i := range items {
		item := &items[i]
		if item.Labels[inventorySynthesisUUIDLabel] == currentUUID {
			continue
		}
		if err := c.downstream.Delete(ctx, item, &client.Preconditions{UID: &item.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting old inventory Secret %q: %w", item.Name, err)
		}
		logr.FromContextOrDiscard(ctx).Info("deleted old inventory chunk", "secretName", item.Name)
	}
	return nil
}
