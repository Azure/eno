package reconciliation

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCrossReconcilerDependencyChecker(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	mgr.Start(t)
	upstreamClient := mgr.GetClient()

	checker := NewCrossReconcilerDependencyChecker(upstreamClient, upstreamClient)

	// Create a composition
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "test-synth"
	comp.Finalizers = []string{"test-finalizer"} // Add finalizer so deletion doesn't complete immediately
	require.NoError(t, upstreamClient.Create(ctx, comp))

	// Now delete it (which sets deletionTimestamp)
	require.NoError(t, upstreamClient.Delete(ctx, comp))

	// Reload to get the deletionTimestamp
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	// Create resource slices with resources at different deletion groups
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "eno.azure.io/v1",
		Kind:       "Composition",
		Name:       comp.Name,
		UID:        comp.UID,
	}})
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.Spec.Resources = []apiv1.Manifest{
		// CRD with deletion-group: -1 (should delete first)
		{
			Manifest: `{
				"apiVersion": "apiextensions.k8s.io/v1",
				"kind": "CustomResourceDefinition",
				"metadata": {
					"name": "mycrds.example.com",
					"annotations": {
						"eno.azure.io/deletion-group": "-1"
					}
				}
			}`,
		},
		// ConfigMap with deletion-group: 0
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "test-cm",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "0"
					}
				}
			}`,
		},
		// Deployment with deletion-group: 1
		{
			Manifest: `{
				"apiVersion": "apps/v1",
				"kind": "Deployment",
				"metadata": {
					"name": "test-deploy",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "1"
					}
				}
			}`,
		},
		// Service with no deletion group
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "Service",
				"metadata": {
					"name": "test-svc",
					"namespace": "default"
				}
			}`,
		},
	}

	// Save status before create - K8s client may clear it during Create()
	initialStatus := []apiv1.ResourceState{
		{Deleted: false}, // CRD
		{Deleted: false}, // ConfigMap
		{Deleted: false}, // Deployment
		{Deleted: false}, // Service
	}
	slice.Status.Resources = initialStatus
	require.NoError(t, upstreamClient.Create(ctx, slice))

	// Restore status after create for in-memory use
	slice.Status.Resources = initialStatus

	// Reload composition to get latest resource version before status update
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	// Update composition to reference this slice
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: "test-uuid",
		ResourceSlices: []*apiv1.ResourceSliceRef{
			{Name: slice.Name},
		},
	}
	require.NoError(t, upstreamClient.Status().Update(ctx, comp))

	// Reload composition to get latest version
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	// Parse resources for testing
	crd, err := testutil.ParseResource(ctx, comp, slice, 0)
	require.NoError(t, err)
	configMap, err := testutil.ParseResource(ctx, comp, slice, 1)
	require.NoError(t, err)
	deployment, err := testutil.ParseResource(ctx, comp, slice, 2)
	require.NoError(t, err)
	service, err := testutil.ParseResource(ctx, comp, slice, 3)
	require.NoError(t, err)

	t.Run("CRD not blocked - lowest deletion group", func(t *testing.T) {
		blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, crd, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.False(t, blocked, "CRD should not be blocked since it has the lowest deletion group")
		assert.Empty(t, reason)
	})

	t.Run("ConfigMap blocked by CRD", func(t *testing.T) {
		blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, configMap, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.True(t, blocked, "ConfigMap should be blocked by CRD")
		assert.Contains(t, reason, "CustomResourceDefinition")
		assert.Contains(t, reason, "deletion-group: -1")
	})

	t.Run("Deployment blocked by CRD and ConfigMap", func(t *testing.T) {
		blocked, _, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, deployment, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.True(t, blocked, "Deployment should be blocked by CRD and/or ConfigMap")
	})

	t.Run("Service not blocked - no deletion group", func(t *testing.T) {
		blocked, _, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, service, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.False(t, blocked, "Service without deletion group should not be blocked")
	})
	// Mark CRD as deleted - just update in-memory for preloaded slice test
	t.Run("After CRD deleted", func(t *testing.T) {
		slice.Status.Resources[0].Deleted = true // CRD deleted

		// Reload composition
		require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

		t.Run("ConfigMap no longer blocked", func(t *testing.T) {
			blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, configMap, []apiv1.ResourceSlice{*slice})
			require.NoError(t, err)
			assert.False(t, blocked, "ConfigMap should not be blocked after CRD is deleted")
			assert.Empty(t, reason)
		})

		t.Run("Deployment still blocked by ConfigMap", func(t *testing.T) {
			blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, deployment, []apiv1.ResourceSlice{*slice})
			require.NoError(t, err)
			assert.True(t, blocked, "Deployment should still be blocked by ConfigMap")
			assert.Contains(t, reason, "ConfigMap")
			assert.Contains(t, reason, "deletion-group: 0")
		})
	})

	// Mark ConfigMap as deleted - just update in-memory for preloaded slice test
	t.Run("After ConfigMap deleted", func(t *testing.T) {
		slice.Status.Resources[1].Deleted = true // ConfigMap deleted

		// Reload composition
		require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

		t.Run("Deployment no longer blocked", func(t *testing.T) {
			blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, deployment, []apiv1.ResourceSlice{*slice})
			require.NoError(t, err)
			assert.False(t, blocked, "Deployment should not be blocked after both CRD and ConfigMap are deleted")
			assert.Empty(t, reason)
		})
	})
}

func TestCrossReconcilerDependencyChecker_NotDuringDeletion(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	mgr.Start(t)
	upstreamClient := mgr.GetClient()

	checker := NewCrossReconcilerDependencyChecker(upstreamClient, upstreamClient)

	// Create a composition WITHOUT deletion timestamp
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "test-synth"
	// No deletion timestamp - composition is active
	require.NoError(t, upstreamClient.Create(ctx, comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "eno.azure.io/v1",
		Kind:       "Composition",
		Name:       comp.Name,
		UID:        comp.UID,
	}})
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.Spec.Resources = []apiv1.Manifest{
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "test-cm",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "1"
					}
				}
			}`,
		},
	}
	slice.Status.Resources = []apiv1.ResourceState{{Deleted: false}}
	require.NoError(t, upstreamClient.Create(ctx, slice))

	// Update status separately
	require.NoError(t, upstreamClient.Status().Update(ctx, slice))

	// Reload composition before status update
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: "test-uuid",
		ResourceSlices: []*apiv1.ResourceSliceRef{
			{Name: slice.Name},
		},
	}
	require.NoError(t, upstreamClient.Status().Update(ctx, comp))

	// Reload composition
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	resource, err := testutil.ParseResource(ctx, comp, slice, 0)
	require.NoError(t, err)

	t.Run("Not blocked during creation", func(t *testing.T) {
		blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, resource, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.False(t, blocked, "Resources should not be blocked by deletion groups during creation")
		assert.Empty(t, reason)
	})
}

func TestCrossReconcilerDependencyChecker_MixedDeletionGroups(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	mgr.Start(t)
	upstreamClient := mgr.GetClient()

	checker := NewCrossReconcilerDependencyChecker(upstreamClient, upstreamClient)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "test-synth"
	comp.Finalizers = []string{"test-finalizer"}
	require.NoError(t, upstreamClient.Create(ctx, comp))

	// Delete it to set deletionTimestamp
	require.NoError(t, upstreamClient.Delete(ctx, comp))

	// Reload to get deletionTimestamp
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice"
	slice.Namespace = "default"
	slice.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "eno.azure.io/v1",
		Kind:       "Composition",
		Name:       comp.Name,
		UID:        comp.UID,
	}})
	slice.Spec.SynthesisUUID = "test-uuid"
	slice.Spec.Resources = []apiv1.Manifest{
		// Resource A: deletion-group -10
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "cm-a",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "-10"
					}
				}
			}`,
		},
		// Resource B: deletion-group 0
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "cm-b",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "0"
					}
				}
			}`,
		},
		// Resource C: deletion-group 5
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "cm-c",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "5"
					}
				}
			}`,
		},
		// Resource D: deletion-group 10
		{
			Manifest: `{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "cm-d",
					"namespace": "default",
					"annotations": {
						"eno.azure.io/deletion-group": "10"
					}
				}
			}`,
		},
	}

	// Save status before create - K8s client may clear it during Create()
	initialStatus := []apiv1.ResourceState{
		{Deleted: false}, // A
		{Deleted: false}, // B
		{Deleted: false}, // C
		{Deleted: false}, // D
	}
	slice.Status.Resources = initialStatus
	require.NoError(t, upstreamClient.Create(ctx, slice))

	// Restore status after create for in-memory use
	slice.Status.Resources = initialStatus

	// Don't update status to cluster - envtest may mutate the in-memory object
	// Since we're using IsBlockedByOtherReconcilersWithPreloadedSlices, we just
	// need the in-memory slice with both manifests and status

	// Reload composition before status update
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: "test-uuid",
		ResourceSlices: []*apiv1.ResourceSliceRef{
			{Name: slice.Name},
		},
	}
	require.NoError(t, upstreamClient.Status().Update(ctx, comp))

	// Reload composition
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	resA, _ := testutil.ParseResource(ctx, comp, slice, 0)
	resB, _ := testutil.ParseResource(ctx, comp, slice, 1)
	_, _ = testutil.ParseResource(ctx, comp, slice, 2) // resC
	resD, _ := testutil.ParseResource(ctx, comp, slice, 3)

	t.Run("Resource D blocked by A, B, C", func(t *testing.T) {
		blocked, _, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, resD, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.True(t, blocked)
	})

	// Delete A - just update in-memory status for preloaded slice test
	slice.Status.Resources[0].Deleted = true
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	t.Run("After A deleted, D still blocked by B and C", func(t *testing.T) {
		blocked, _, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, resD, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.True(t, blocked)
	})

	// Delete B and C - just update in-memory status for preloaded slice test
	slice.Status.Resources[1].Deleted = true
	slice.Status.Resources[2].Deleted = true
	require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

	t.Run("After A, B, C deleted, D not blocked", func(t *testing.T) {
		blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, resD, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.False(t, blocked)
		assert.Empty(t, reason)
	})

	t.Run("A never blocked", func(t *testing.T) {
		blocked, _, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, resA, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.False(t, blocked)
	})

	t.Run("B only blocked by A initially", func(t *testing.T) {
		// Reset to initial state - just update in-memory status for preloaded slice test
		slice.Status.Resources[0].Deleted = false
		require.NoError(t, upstreamClient.Get(ctx, client.ObjectKeyFromObject(comp), comp))

		blocked, reason, err := checker.IsBlockedByOtherReconcilersWithPreloadedSlices(ctx, comp, resB, []apiv1.ResourceSlice{*slice})
		require.NoError(t, err)
		assert.True(t, blocked)
		assert.Contains(t, reason, "cm-a")
	})
}
