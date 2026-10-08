package tombstonerecovery

import (
	"context"
	"fmt"
	"maps"
	"reflect"

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
	existingTime, err := inventorySynthesized(existing)
	if err != nil {
		return err
	}
	intendedTime, err := inventorySynthesized(intended)
	if err != nil {
		return err
	}
	if existingTime.After(intendedTime) {
		return fmt.Errorf("refusing to overwrite inventory Secret %q with an older source timestamp", existing.Name)
	}
	after := existing.DeepCopy()
	after.Type = intended.Type
	after.Data = intended.Data
	if after.Labels == nil {
		after.Labels = map[string]string{}
	}
	if after.Annotations == nil {
		after.Annotations = map[string]string{}
	}
	maps.Copy(after.Labels, intended.Labels)
	maps.Copy(after.Annotations, intended.Annotations)
	if reflect.DeepEqual(existing, after) {
		return nil
	}
	// One synthesis describes one immutable snapshot, including its chunk boundaries.
	if existing.Annotations[inventorySynthesisUUIDAnnotation] == intended.Annotations[inventorySynthesisUUIDAnnotation] {
		return fmt.Errorf("inventory Secret %q has different contents or metadata for the same synthesis UUID", existing.Name)
	}
	if _, err := c.getCurrentComposition(ctx, comp, true); err != nil {
		return err
	}
	// Update carries the resourceVersion from Get; conflicting updates retry through reconciliation.
	if err := c.downstream.Update(ctx, after); err != nil {
		return fmt.Errorf("updating inventory Secret %q: %w", existing.Name, err)
	}
	logr.FromContextOrDiscard(ctx).Info("updated inventory chunk", "secretName", existing.Name)
	return nil
}
