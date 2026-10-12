package tombstonerecovery

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// cleanupInventory deletes every inventory Secret for a deleting Composition's lineage.
// Best effort: it does not block Composition deletion.
func (c *tombstoneRecoveryController) cleanupInventory(ctx context.Context, comp *apiv1.Composition) error {
	items, err := c.readInventories(ctx, comp)
	if err != nil {
		return err
	}
	for i := range items {
		item := &items[i]
		if err := c.downstream.Delete(ctx, item); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting inventory Secret %q: %w", item.Name, err)
		}
		logr.FromContextOrDiscard(ctx).Info("deleted inventory chunk", "secretName", item.Name)
	}
	return nil
}
