package reconciliation

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CrossReconcilerDependencyChecker checks if resources in other reconcilers
// that have lower deletion groups are still being deleted, which would block this resource.
type CrossReconcilerDependencyChecker struct {
	client    client.Client
	apiReader client.Reader // Non-cached reader to get full ResourceSlice manifests
}

func NewCrossReconcilerDependencyChecker(client client.Client, apiReader client.Reader) *CrossReconcilerDependencyChecker {
	return &CrossReconcilerDependencyChecker{
		client:    client,
		apiReader: apiReader,
	}
}

// IsBlockedByOtherReconcilers checks if a resource should be blocked from reconciliation
// because resources with lower deletion groups (potentially in other reconcilers) haven't been deleted yet.
func (c *CrossReconcilerDependencyChecker) IsBlockedByOtherReconcilers(ctx context.Context, comp *apiv1.Composition, res *resource.Resource) (blocked bool, reason string, err error) {
	return c.isBlockedByOtherReconcilersWithSlices(ctx, comp, res, nil)
}

// isBlockedByOtherReconcilersWithSlices is the internal implementation that optionally accepts pre-loaded slices (for testing)
func (c *CrossReconcilerDependencyChecker) isBlockedByOtherReconcilersWithSlices(ctx context.Context, comp *apiv1.Composition, res *resource.Resource, preloadedSlices []apiv1.ResourceSlice) (blocked bool, reason string, err error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Only check during deletion
	if !res.CompositionDeleted() {
		return false, "", nil
	}

	// Only check if this resource has a deletion group
	myDeletionGroup := res.DeletionGroup()
	if myDeletionGroup == nil {
		return false, "", nil
	}

	// Get current synthesis to check all resource slices
	if comp.Status.CurrentSynthesis == nil {
		return false, "", nil
	}

	// Check all resource slices for resources with lower deletion groups
	for idx, sliceRef := range comp.Status.CurrentSynthesis.ResourceSlices {
		var slice *apiv1.ResourceSlice
		
		// Use preloaded slices if provided (for testing), otherwise query API
		if preloadedSlices != nil && idx < len(preloadedSlices) {
			slice = &preloadedSlices[idx]
		} else {
			slice = &apiv1.ResourceSlice{}
			sliceKey := client.ObjectKey{Name: sliceRef.Name, Namespace: comp.Namespace}
			// Use apiReader to bypass cache and get full manifests (informer cache prunes manifests to save memory)
			if err := c.apiReader.Get(ctx, sliceKey, slice); err != nil {
				if client.IgnoreNotFound(err) != nil {
					return false, "", fmt.Errorf("failed to get resource slice: %w", err)
				}
				continue
			}
		}

		// Check each resource in the slice
		for i := range slice.Spec.Resources {
			// Parse the resource to get its deletion group
			otherRes, err := resource.FromSlice(ctx, comp, slice, i)
			if err != nil {
				logger.V(1).Info("failed to parse resource from slice", "error", err, "sliceName", slice.Name, "index", i)
				continue
			}

			otherDeletionGroup := otherRes.DeletionGroup()
			if otherDeletionGroup == nil {
				continue // No deletion group means no ordering
			}

			// If the other resource has a lower deletion group, check if it's fully deleted
			if *otherDeletionGroup < *myDeletionGroup {
				// Check the status
				var state apiv1.ResourceState
				if len(slice.Status.Resources) > i {
					state = slice.Status.Resources[i]
				}

				// If the resource with lower deletion group is not yet deleted, we're blocked
				if !state.Deleted {
					reason := fmt.Sprintf("blocked by resource %s/%s (deletion-group: %d) which hasn't been deleted yet",
						otherRes.Ref.Kind, otherRes.Ref.Name, *otherDeletionGroup)
					logger.Info("resource blocked by cross-reconciler dependency",
						"blockedResource", res.Ref.String(),
						"blockingResource", otherRes.Ref.String(),
						"myDeletionGroup", *myDeletionGroup,
						"blockingDeletionGroup", *otherDeletionGroup,
					)
					return true, reason, nil
				}
			}
		}
	}

	return false, "", nil
}

// IsBlockedByOtherReconcilersWithPreloadedSlices is a test helper that allows passing slices directly
// to avoid issues with envtest pruning ResourceSlice manifests
func (c *CrossReconcilerDependencyChecker) IsBlockedByOtherReconcilersWithPreloadedSlices(ctx context.Context, comp *apiv1.Composition, res *resource.Resource, slices []apiv1.ResourceSlice) (blocked bool, reason string, err error) {
	return c.isBlockedByOtherReconcilersWithSlices(ctx, comp, res, slices)
}
