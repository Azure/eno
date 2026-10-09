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
	if _, err := c.getCurrentComposition(ctx, comp, true); err != nil {
		return err
	}
	slices, err := c.loadCurrentSynthesisResourceSlices(ctx, comp)
	if err != nil {
		return err
	}
	chunks, err := makeInventory(comp, slices)
	if err != nil {
		return fmt.Errorf("building inventory: %w", err)
	}
	for i := range chunks {
		if err := c.writeInventoryChunk(ctx, comp, &chunks[i]); err != nil {
			return err
		}
	}
	if err := c.verifyInventorySnapshot(ctx, comp, chunks); err != nil {
		return err
	}
	if err := c.deleteOldInventorySnapshots(ctx, comp, chunks[0].Annotations[inventorySynthesisUUIDAnnotation]); err != nil {
		return err
	}
	logr.FromContextOrDiscard(ctx).V(1).Info("inventory snapshot persisted", "chunkCount", len(chunks))
	return nil
}

func (c *tombstoneRecoveryController) writeInventoryChunk(ctx context.Context, comp *apiv1.Composition, intended *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := c.downstream.Get(ctx, client.ObjectKeyFromObject(intended), existing)
	if apierrors.IsNotFound(err) {
		if _, err := c.getCurrentComposition(ctx, comp, true); err != nil {
			return err
		}
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
		existing.Name, intended.Annotations[inventorySynthesisUUIDAnnotation])
}

func inventorySecretMatches(existing, intended *corev1.Secret) bool {
	return existing.Type == intended.Type &&
		reflect.DeepEqual(existing.Data, intended.Data) &&
		reflect.DeepEqual(existing.Labels, intended.Labels) &&
		reflect.DeepEqual(existing.Annotations, intended.Annotations)
}

func (c *tombstoneRecoveryController) verifyInventorySnapshot(ctx context.Context, comp *apiv1.Composition, intended []corev1.Secret) error {
	items, err := c.readInventories(ctx, comp)
	if err != nil {
		return err
	}
	uuid := intended[0].Annotations[inventorySynthesisUUIDAnnotation]
	actual := make([]corev1.Secret, 0, len(intended))
	for i := range items {
		if items[i].Annotations[inventorySynthesisUUIDAnnotation] == uuid {
			actual = append(actual, items[i])
		}
	}
	if _, err := validateInventory(actual); err != nil {
		return fmt.Errorf("verifying inventory snapshot: %w", err)
	}
	byName := map[string]*corev1.Secret{}
	for i := range actual {
		byName[actual[i].Name] = &actual[i]
	}
	for i := range intended {
		existing := byName[intended[i].Name]
		if existing == nil || !inventorySecretMatches(existing, &intended[i]) {
			return fmt.Errorf("verifying inventory snapshot: Secret %q does not match intended contents", intended[i].Name)
		}
	}
	return nil
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
		if item.Annotations[inventorySynthesisUUIDAnnotation] == currentUUID {
			continue
		}
		if _, err := c.getCurrentComposition(ctx, comp, true); err != nil {
			return err
		}
		if err := c.downstream.Delete(ctx, item, &client.Preconditions{UID: &item.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting old inventory Secret %q: %w", item.Name, err)
		}
		logr.FromContextOrDiscard(ctx).Info("deleted old inventory chunk", "secretName", item.Name)
	}
	return nil
}
