package tombstonerecovery

import (
	"context"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
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
		if err := deleteInventorySecret(ctx, c.downstream, item); err != nil {
			return err
		}
		logr.FromContextOrDiscard(ctx).Info("deleted inventory chunk", "secretName", item.Name)
	}
	return nil
}
