package backup

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestBackupControllerCompositionWatchAdvancesPhases(t *testing.T) {
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, Options{Enabled: true, Downstream: mgr.DownstreamRestConfig}))
	mgr.Start(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	upstream, err := client.NewWithWatch(mgr.RestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	downstream, err := client.New(mgr.DownstreamRestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "watch-phases", Namespace: "default"},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "test"}},
	}
	require.NoError(t, upstream.Create(ctx, comp))
	events, err := upstream.Watch(ctx, &apiv1.CompositionList{}, client.InNamespace(comp.Namespace),
		client.MatchingFields{"metadata.name": comp.Name},
		&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: comp.ResourceVersion}})
	require.NoError(t, err)
	defer events.Stop()

	// No ResourceSlices or other controllers can enqueue work between these status transitions.
	now := metav1.Now()
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: controllerTestUUID(2), Synthesized: &now, Ready: &now,
		TombstoneRecoveryRequired: true,
	}
	require.NoError(t, upstream.Status().Update(ctx, comp))

	var reasons []string
	for len(reasons) < 1 {
		select {
		case event, ok := <-events.ResultChan():
			require.True(t, ok, "Composition watch closed before recovery completed")
			if event.Type == watch.Error {
				t.Fatalf("Composition watch failed: %v", apierrors.FromObject(event.Object))
			}
			observed, ok := event.Object.(*apiv1.Composition)
			require.True(t, ok, "unexpected watch object %T", event.Object)
			syn := observed.Status.CurrentSynthesis
			if syn == nil || syn.TombstoneRecoveryFinished == nil {
				continue
			}
			status := syn.TombstoneRecoveryFinished
			require.Equal(t, controllerTestUUID(2), status.SynthesisUUID)
			require.True(t, status.Status)
			reasons = append(reasons, status.Reason)
		case <-ctx.Done():
			t.Fatalf("recovery did not advance through Composition watch events: %v (observed %v)", ctx.Err(), reasons)
		}
	}
	require.Equal(t, []string{reasonInventoryNotFound}, reasons)

	inventory := &corev1.ConfigMap{}
	key := client.ObjectKey{Namespace: inventoryNamespace, Name: inventoryName(comp, controllerTestUUID(2))}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		require.NoError(c, downstream.Get(ctx, key, inventory))
	}, 10*time.Second, 10*time.Millisecond, "terminal status watch event must trigger inventory recording")
	resources, err := decodeInventorySnapshot(comp, *inventory)
	require.NoError(t, err)
	require.Empty(t, resources)
}

func TestBackupControllerAPIPreconditions(t *testing.T) {
	mgr := testutil.NewManager(t)
	upstream, err := client.New(mgr.RestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	downstream, err := client.New(mgr.DownstreamRestConfig, client.Options{Scheme: mgr.GetScheme()})
	require.NoError(t, err)
	c := &backupController{client: upstream, reader: upstream, downstream: downstream}
	ctx := t.Context()
	comp := &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{Name: "api-preconditions", Namespace: "default"},
		Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "test"}},
	}
	require.NoError(t, upstream.Create(ctx, comp))
	now := metav1.Now()
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: controllerTestUUID(2), Synthesized: &now, Ready: &now,
	}
	require.NoError(t, upstream.Status().Update(ctx, comp))

	for _, field := range []string{"uid", "resourceVersion", "currentUUID"} {
		t.Run("status-"+field, func(t *testing.T) {
			before := comp.DeepCopy()
			switch field {
			case "uid":
				before.UID = "stale-uid"
			case "resourceVersion":
				before.ResourceVersion = "0"
			case "currentUUID":
				before.Status.CurrentSynthesis.UUID = controllerTestUUID(1)
			}
			require.Error(t, c.markTombstoneRecoveryFinished(ctx, before, reasonNotNeeded, "", nil),
				"each JSON patch test must independently reject a stale operation")
			got := &apiv1.Composition{}
			require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(comp), got))
			assert.Equal(t, comp, got)
		})
	}

	t.Run("slice-resourceVersion", func(t *testing.T) {
		slice := &apiv1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{Name: "optimistic-append", Namespace: comp.Namespace},
			Spec: apiv1.ResourceSliceSpec{
				SynthesisUUID: comp.Status.CurrentSynthesis.UUID,
				Resources:     []apiv1.Manifest{inventoryTestManifest(t, controllerTestResource("desired"))},
			},
		}
		require.NoError(t, upstream.Create(ctx, slice))
		stale := slice.DeepCopy()
		slice.Spec.Resources = append(slice.Spec.Resources, inventoryTestManifest(t, controllerTestResource("concurrent")))
		require.NoError(t, upstream.Update(ctx, slice))
		tombstone := inventoryTestManifest(t, controllerTestResource("removed"))
		tombstone.Deleted = true
		refs, err := c.writeTombstones(ctx, comp, []apiv1.ResourceSlice{*stale}, []apiv1.Manifest{tombstone})
		require.True(t, apierrors.IsConflict(err), "expected optimistic-lock conflict, got %v", err)
		assert.Nil(t, refs)
		got := &apiv1.ResourceSlice{}
		require.NoError(t, upstream.Get(ctx, client.ObjectKeyFromObject(slice), got))
		assert.Equal(t, slice, got, "a stale append must not overwrite concurrent resources")
	})

	for _, mode := range []string{"mutation", "replacement"} {
		t.Run("inventory-delete-"+mode, func(t *testing.T) {
			oldComp := comp.DeepCopy()
			if mode == "mutation" {
				oldComp.Status.CurrentSynthesis.UUID = controllerTestUUID(3)
			} else {
				oldComp.Status.CurrentSynthesis.UUID = controllerTestUUID(4)
			}
			item, err := makeInventory(oldComp, nil)
			require.NoError(t, err)
			require.NoError(t, downstream.Create(ctx, item))
			listed := item.DeepCopy()
			if mode == "mutation" {
				item.Annotations["test.example/changed"] = "true"
				require.NoError(t, downstream.Update(ctx, item))
			} else {
				require.NoError(t, downstream.Delete(ctx, item))
				item, err = makeInventory(oldComp, nil)
				require.NoError(t, err)
				require.NoError(t, downstream.Create(ctx, item))
				require.NotEqual(t, listed.UID, item.UID)
				// Isolate the UID precondition: the resourceVersion is otherwise current.
				listed.ResourceVersion = item.ResourceVersion
			}
			keep, err := makeInventory(comp, nil)
			require.NoError(t, err)
			err = c.deleteOtherInventories(ctx, comp, keep, []corev1.ConfigMap{*listed})
			require.True(t, apierrors.IsConflict(err), "expected delete precondition conflict, got %v", err)
			got := &corev1.ConfigMap{}
			require.NoError(t, downstream.Get(ctx, client.ObjectKeyFromObject(item), got))
			assert.Equal(t, item, got, "concurrent inventory changes must survive cleanup")
		})
	}
}
