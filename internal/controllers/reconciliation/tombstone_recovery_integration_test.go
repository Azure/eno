package reconciliation

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/execution"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func TestRecoveryFlagAPIPersistence(t *testing.T) {
	mgr := testutil.NewManager(t)
	fields := []string{"currentSynthesis", "inFlightSynthesis", "previousSynthesis"}
	for _, tc := range []struct {
		name          string
		flags         [3]bool
		explicitFalse bool
	}{
		{name: "current", flags: [3]bool{true, false, false}},
		{name: "inflight", flags: [3]bool{false, true, false}},
		{name: "previous", flags: [3]bool{false, false, true}},
		{name: "all", flags: [3]bool{true, true, true}},
		{name: "false", explicitFalse: true},
		{name: "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := &apiv1.Composition{ObjectMeta: metav1.ObjectMeta{
				Name: "flag-" + tc.name, Namespace: "default",
			}}
			comp.Spec.Synthesizer.Name = "schema-test"
			require.NoError(t, mgr.GetClient().Create(t.Context(), comp))
			comp.Status = apiv1.CompositionStatus{
				CurrentSynthesis:  &apiv1.Synthesis{UUID: "current", TombstoneRecoveryRequired: tc.flags[0]},
				InFlightSynthesis: &apiv1.Synthesis{UUID: "inflight", TombstoneRecoveryRequired: tc.flags[1]},
				PreviousSynthesis: &apiv1.Synthesis{UUID: "previous", TombstoneRecoveryRequired: tc.flags[2]},
			}
			require.NoError(t, mgr.GetClient().Status().Update(t.Context(), comp))

			wire := &unstructured.Unstructured{}
			wire.SetGroupVersionKind(apiv1.SchemeGroupVersion.WithKind("Composition"))
			key := client.ObjectKeyFromObject(comp)
			require.NoError(t, mgr.GetAPIReader().Get(t.Context(), key, wire))
			if tc.explicitFalse {
				// A typed false is omitted by JSON encoding; exercise explicit false on the wire too.
				for _, field := range fields {
					require.NoError(t, unstructured.SetNestedField(wire.Object, false, "status", field, "tombstoneRecoveryRequired"))
				}
				require.NoError(t, mgr.GetClient().Status().Update(t.Context(), wire))
				require.NoError(t, mgr.GetAPIReader().Get(t.Context(), key, wire))
			}

			stored := recoveryIntegrationReadComposition(t, mgr, key)
			syntheses := []*apiv1.Synthesis{
				stored.Status.CurrentSynthesis, stored.Status.InFlightSynthesis, stored.Status.PreviousSynthesis,
			}
			for i, syn := range syntheses {
				require.NotNil(t, syn, fields[i])
				require.Equal(t, tc.flags[i], syn.TombstoneRecoveryRequired, fields[i])
				flag, found, err := unstructured.NestedBool(wire.Object, "status", fields[i], "tombstoneRecoveryRequired")
				require.NoError(t, err)
				require.Equal(t, tc.flags[i], flag, fields[i])
				if tc.flags[i] {
					require.True(t, found, "generated CRD must preserve %s.tombstoneRecoveryRequired", fields[i])
				}
			}
		})
	}
}

func TestRecoveryIntegrationEmptyCurrentReference(t *testing.T) {
	mgr := testutil.NewManager(t)
	var calls atomic.Int32
	recoveryIntegrationControllers(t, mgr, false, func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		calls.Add(1)
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{
			recoveryIntegrationConfigMap("replacement", map[string]string{"value": "ready"}),
		}}, nil
	})
	_, comp := recoveryIntegrationSeedLegacy(t, mgr)
	comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{}}
	require.NoError(t, mgr.GetClient().Status().Update(t.Context(), comp))
	old := comp.Status.CurrentSynthesis.DeepCopy()
	require.False(t, old.TombstoneRecoveryRequired)

	// Nothing changes the spec or requests synthesis in the fixture. The empty
	// current reference must request it, and then be skipped as previous history.
	mgr.Start(t)
	stored := recoveryIntegrationWaitReady(t, mgr, client.ObjectKeyFromObject(comp), old.UUID)
	require.True(t, stored.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	require.Equal(t, old.UUID, stored.Status.PreviousSynthesis.UUID)
	require.Equal(t, old.ResourceSlices, stored.Status.PreviousSynthesis.ResourceSlices)
	require.False(t, stored.Status.PreviousSynthesis.TombstoneRecoveryRequired)
	recoveryIntegrationAssertInventory(t, mgr, stored, map[string]bool{"replacement": false})
	recoveryIntegrationAssertConfigMap(t, mgr, "replacement", map[string]string{"value": "ready"})
	require.EqualValues(t, 1, calls.Load())
}

func TestRecoveryIntegrationPartialHistoryLifecycle(t *testing.T) {
	mgr := testutil.NewManager(t)
	var calls atomic.Int32
	recoveryIntegrationControllers(t, mgr, false, func(_ context.Context, syn *apiv1.Synthesizer, _ *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		calls.Add(1)
		value := "recovered"
		if syn.Spec.Image == "healthy-successor" {
			value = "healthy"
		}
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{
			recoveryIntegrationConfigMap("a", map[string]string{"value": value}),
			recoveryIntegrationConfigMap("d", map[string]string{"value": value}),
		}}, nil
	})
	syn, comp := recoveryIntegrationSeedLegacy(t, mgr)
	available := recoveryIntegrationCreateSlice(t, mgr, comp, "available",
		recoveryIntegrationConfigMap("a", map[string]string{"value": "old"}),
		recoveryIntegrationConfigMap("b", map[string]string{"value": "old"}))
	lost := recoveryIntegrationCreateSlice(t, mgr, comp, "lost",
		recoveryIntegrationConfigMap("c", map[string]string{"value": "unknown-history"}))
	when := metav1.NewTime(time.Now().Add(-time.Minute))
	for _, slice := range []*apiv1.ResourceSlice{available, lost} {
		for range slice.Spec.Resources {
			slice.Status.Resources = append(slice.Status.Resources, apiv1.ResourceState{Reconciled: true, Ready: &when})
		}
		require.NoError(t, mgr.GetClient().Status().Update(t.Context(), slice))
	}
	comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{
		{Name: available.Name}, {Name: lost.Name},
	}
	comp.Status.CurrentSynthesis.Reconciled = &when
	comp.Status.CurrentSynthesis.Ready = &when
	require.NoError(t, mgr.GetClient().Status().Update(t.Context(), comp))
	old := comp.Status.CurrentSynthesis.DeepCopy()
	require.False(t, old.TombstoneRecoveryRequired)
	for _, obj := range []*unstructured.Unstructured{
		recoveryIntegrationConfigMap("a", map[string]string{"value": "old"}),
		recoveryIntegrationConfigMap("b", map[string]string{"value": "old"}),
		recoveryIntegrationConfigMap("c", map[string]string{"value": "unknown-history"}),
	} {
		require.NoError(t, mgr.DownstreamClient.Create(t.Context(), obj))
	}
	// Incident injection: lose only C's published slice, not the distinct A/B slice.
	recoveryIntegrationLoseSlice(t, mgr, client.ObjectKeyFromObject(lost))
	mgr.Start(t)

	key := client.ObjectKeyFromObject(comp)
	recovered := recoveryIntegrationWaitReady(t, mgr, key, old.UUID)
	require.True(t, recovered.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	require.Equal(t, old.UUID, recovered.Status.PreviousSynthesis.UUID)
	require.Equal(t, old.ResourceSlices, recovered.Status.PreviousSynthesis.ResourceSlices)
	require.False(t, recovered.Status.PreviousSynthesis.TombstoneRecoveryRequired)
	recoveryIntegrationAssertInventory(t, mgr, recovered, map[string]bool{"a": false, "b": true, "d": false})
	recoveryIntegrationAssertConfigMap(t, mgr, "a", map[string]string{"value": "recovered"})
	recoveryIntegrationAssertConfigMap(t, mgr, "d", map[string]string{"value": "recovered"})
	recoveryIntegrationAssertAbsent(t, mgr, "b")
	// Part 1 cannot discover C's identity once its only inventory has been lost.
	recoveryIntegrationAssertConfigMap(t, mgr, "c", map[string]string{"value": "unknown-history"})

	recoveredSnapshot := recovered.Status.CurrentSynthesis.DeepCopy()
	require.NoError(t, retry.RetryOnConflict(testutil.Backoff, func() error {
		fresh := &apiv1.Synthesizer{}
		if err := mgr.GetAPIReader().Get(t.Context(), client.ObjectKeyFromObject(syn), fresh); err != nil {
			return err
		}
		fresh.Spec.Image = "healthy-successor"
		return mgr.GetClient().Update(t.Context(), fresh)
	}))
	healthy := recoveryIntegrationWaitReady(t, mgr, key, recoveredSnapshot.UUID)
	require.True(t, healthy.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	require.True(t, healthy.Status.PreviousSynthesis.TombstoneRecoveryRequired)
	require.Equal(t, recoveredSnapshot.UUID, healthy.Status.PreviousSynthesis.UUID)
	require.Equal(t, recoveredSnapshot.ResourceSlices, healthy.Status.PreviousSynthesis.ResourceSlices)
	recoveryIntegrationAssertInventory(t, mgr, healthy, map[string]bool{"a": false, "d": false})
	recoveryIntegrationAssertConfigMap(t, mgr, "a", map[string]string{"value": "healthy"})
	recoveryIntegrationAssertConfigMap(t, mgr, "d", map[string]string{"value": "healthy"})
	require.EqualValues(t, 2, calls.Load())

	// No kube-controller-manager runs in envtest. Eno itself must remove known
	// outputs and release its Composition and ResourceSlice cleanup finalizers.
	require.Contains(t, healthy.Finalizers, "eno.azure.io/cleanup")
	require.NoError(t, mgr.GetClient().Delete(t.Context(), healthy))
	testutil.Eventually(t, func() bool {
		return apierrors.IsNotFound(mgr.GetAPIReader().Get(t.Context(), key, &apiv1.Composition{}))
	})
	recoveryIntegrationAssertAbsent(t, mgr, "a")
	recoveryIntegrationAssertAbsent(t, mgr, "b")
	recoveryIntegrationAssertAbsent(t, mgr, "d")
	recoveryIntegrationAssertConfigMap(t, mgr, "c", map[string]string{"value": "unknown-history"})
	testutil.Eventually(t, func() bool {
		list := &apiv1.ResourceSliceList{}
		if err := mgr.GetAPIReader().List(t.Context(), list, client.InNamespace(comp.Namespace)); err != nil {
			return false
		}
		return len(list.Items) == 0
	})
}

func TestRecoveryIntegrationAllPreviousHistoryUnavailableColdStart(t *testing.T) {
	mgr := testutil.NewManager(t)
	var calls atomic.Int32
	handler := func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		calls.Add(1)
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{
			recoveryIntegrationConfigMap("new-desired", map[string]string{"value": "ready"}),
		}}, nil
	}
	recoveryIntegrationControllers(t, mgr, false, handler)
	syn, comp := recoveryIntegrationSeedLegacy(t, mgr)
	comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{Name: "lost-one"}, {}, {Name: "lost-two"}}
	old := comp.Status.CurrentSynthesis.DeepCopy()
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{
		UUID: "published-before-restart", Initialized: &metav1.Time{Time: time.Now()},
		ObservedCompositionGeneration: comp.Generation,
	}
	require.NoError(t, mgr.GetClient().Status().Update(t.Context(), comp))

	// Publish with the real executor before any informer/resource cache starts:
	// the replacement and completely unavailable previous history are read cold.
	executor := &execution.Executor{Reader: mgr.GetAPIReader(), Writer: mgr.GetClient(), Handler: handler}
	require.NoError(t, executor.Synthesize(t.Context(), &execution.Env{
		CompositionName: comp.Name, CompositionNamespace: comp.Namespace,
		SynthesisUUID: comp.Status.InFlightSynthesis.UUID, Image: syn.Spec.Image,
	}))
	key := client.ObjectKeyFromObject(comp)
	published := recoveryIntegrationReadComposition(t, mgr, key)
	require.True(t, published.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	require.False(t, old.TombstoneRecoveryRequired)
	require.Equal(t, old.ResourceSlices, published.Status.PreviousSynthesis.ResourceSlices)
	publishedUUID := published.Status.CurrentSynthesis.UUID
	mgr.Start(t)

	stored := recoveryIntegrationWaitReady(t, mgr, key, old.UUID)
	require.Equal(t, publishedUUID, stored.Status.CurrentSynthesis.UUID)
	require.True(t, stored.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	recoveryIntegrationAssertInventory(t, mgr, stored, map[string]bool{"new-desired": false})
	recoveryIntegrationAssertConfigMap(t, mgr, "new-desired", map[string]string{"value": "ready"})
	// Deliver more Composition events without changing its generation. Missing
	// previous inventory must not request another synthesis on those reconciles.
	for i := range 3 {
		marker := fmt.Sprint(i)
		require.NoError(t, retry.RetryOnConflict(testutil.Backoff, func() error {
			fresh := recoveryIntegrationReadComposition(t, mgr, key)
			if fresh.Annotations == nil {
				fresh.Annotations = map[string]string{}
			}
			fresh.Annotations["recovery-integration-checkpoint"] = marker
			return mgr.GetClient().Update(t.Context(), fresh)
		}))
		testutil.Eventually(t, func() bool {
			cached := &apiv1.Composition{}
			return mgr.GetClient().Get(t.Context(), key, cached) == nil &&
				cached.Annotations["recovery-integration-checkpoint"] == marker
		})
		stored = recoveryIntegrationWaitReady(t, mgr, key, old.UUID)
		require.Equal(t, publishedUUID, stored.Status.CurrentSynthesis.UUID)
		require.EqualValues(t, 1, calls.Load())
	}
}

func TestRecoveryIntegrationNoCurrentSynthesisWithOrphans(t *testing.T) {
	mgr := testutil.NewManager(t)
	recoveryIntegrationControllers(t, mgr, false, func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		return &krmv1.ResourceList{Items: []*unstructured.Unstructured{
			recoveryIntegrationConfigMap("desired", map[string]string{"value": "new"}),
		}}, nil
	})
	require.NoError(t, mgr.DownstreamClient.Create(t.Context(), recoveryIntegrationConfigMap("desired", map[string]string{"value": "old"})))
	require.NoError(t, mgr.DownstreamClient.Create(t.Context(), recoveryIntegrationConfigMap("orphan", map[string]string{"value": "unrelated"})))
	_, comp := writeGenericComposition(t, mgr.GetClient())
	require.Nil(t, recoveryIntegrationReadComposition(t, mgr, client.ObjectKeyFromObject(comp)).Status.CurrentSynthesis)
	mgr.Start(t)
	stored := recoveryIntegrationWaitReady(t, mgr, client.ObjectKeyFromObject(comp), "")
	require.True(t, stored.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	require.Nil(t, stored.Status.PreviousSynthesis)
	recoveryIntegrationAssertInventory(t, mgr, stored, map[string]bool{"desired": false})
	recoveryIntegrationAssertConfigMap(t, mgr, "desired", map[string]string{"value": "new"})
	recoveryIntegrationAssertConfigMap(t, mgr, "orphan", map[string]string{"value": "unrelated"})
}

func TestRecoveryIntegrationAvailableHistoryFieldOwnership(t *testing.T) {
	for _, disableSSA := range []bool{false, true} {
		t.Run(fmt.Sprintf("DisableSSA=%t", disableSSA), func(t *testing.T) {
			mgr := testutil.NewManager(t)
			if !disableSSA {
				requireSSA(t, mgr)
			}
			var calls atomic.Int32
			recoveryIntegrationControllers(t, mgr, disableSSA, func(context.Context, *apiv1.Synthesizer, *krmv1.ResourceList) (*krmv1.ResourceList, error) {
				calls.Add(1)
				return &krmv1.ResourceList{Items: []*unstructured.Unstructured{
					recoveryIntegrationConfigMap("available", map[string]string{"keep": "after"}),
				}}, nil
			})
			_, comp := recoveryIntegrationSeedLegacy(t, mgr)
			available := recoveryIntegrationCreateSlice(t, mgr, comp, "available-history",
				recoveryIntegrationConfigMap("available", map[string]string{"keep": "before", "remove": "managed"}))
			lost := recoveryIntegrationCreateSlice(t, mgr, comp, "unrelated-history",
				recoveryIntegrationConfigMap("unrelated", map[string]string{"value": "survivor"}))
			comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{Name: available.Name}, {Name: lost.Name}}
			require.NoError(t, mgr.GetClient().Status().Update(t.Context(), comp))
			old := comp.Status.CurrentSynthesis.DeepCopy()

			// Force an actual Eno apply before handing off the unrelated field:
			// an already-matching fixture can skip apply and never establish ownership.
			require.NoError(t, mgr.DownstreamClient.Create(t.Context(),
				recoveryIntegrationConfigMap("available", map[string]string{"keep": "unmanaged", "remove": "unmanaged"})))
			mgr.Start(t)
			initial := recoveryIntegrationWaitReady(t, mgr, client.ObjectKeyFromObject(comp), "")
			require.Equal(t, old.UUID, initial.Status.CurrentSynthesis.UUID)
			require.False(t, initial.Status.CurrentSynthesis.TombstoneRecoveryRequired)
			require.Zero(t, calls.Load(), "the intact legacy synthesis must not be resynthesized")
			recoveryIntegrationAssertConfigMap(t, mgr, "available", map[string]string{"keep": "before", "remove": "managed"})
			if !disableSSA {
				managed := &corev1.ConfigMap{}
				require.NoError(t, mgr.DownstreamClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "available"}, managed))
				owned := false
				for _, field := range managed.ManagedFields {
					if field.Manager == "eno" && field.FieldsV1 != nil {
						owned = true
						require.Contains(t, string(field.FieldsV1.Raw), `"f:remove"`)
					}
				}
				require.True(t, owned, "real Eno apply must establish ownership before the incident")
			}
			external := recoveryIntegrationConfigMap("available", map[string]string{"external": "outside"})
			if disableSSA {
				require.NoError(t, mgr.DownstreamClient.Patch(t.Context(), external,
					client.RawPatch(types.MergePatchType, []byte(`{"data":{"external":"outside"}}`)), client.FieldOwner("recovery-external")))
			} else {
				require.NoError(t, mgr.DownstreamClient.Patch(t.Context(), external,
					client.Apply, client.FieldOwner("recovery-external")))
			}
			recoveryIntegrationAssertConfigMap(t, mgr, "available", map[string]string{
				"keep": "before", "remove": "managed", "external": "outside",
			})
			recoveryIntegrationLoseSlice(t, mgr, client.ObjectKeyFromObject(lost))

			recovered := recoveryIntegrationWaitReady(t, mgr, client.ObjectKeyFromObject(comp), old.UUID)
			require.True(t, recovered.Status.CurrentSynthesis.TombstoneRecoveryRequired)
			require.False(t, recovered.Status.PreviousSynthesis.TombstoneRecoveryRequired)
			require.Equal(t, old.ResourceSlices, recovered.Status.PreviousSynthesis.ResourceSlices)
			recoveryIntegrationAssertInventory(t, mgr, recovered, map[string]bool{"available": false})
			recoveryIntegrationAssertConfigMap(t, mgr, "available", map[string]string{"keep": "after", "external": "outside"})
			recoveryIntegrationAssertConfigMap(t, mgr, "unrelated", map[string]string{"value": "survivor"})
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func recoveryIntegrationControllers(t *testing.T, mgr *testutil.Manager, disableSSA bool, handler execution.SynthesizerHandle) {
	t.Helper()
	registerControllers(t, mgr)
	testutil.WithFakeExecutor(t, mgr, handler)
	downstream := rest.CopyConfig(mgr.DownstreamRestConfig)
	downstream.QPS = 200
	require.NoError(t, New(mgr.Manager, Options{
		Manager: mgr.Manager, Downstream: downstream,
		Timeout: time.Minute, ReadinessPollInterval: time.Hour,
		DisableServerSideApply: disableSSA || mgr.NoSsaSupport,
		WriteBuffer:            flowcontrol.NewResourceSliceWriteBufferForManager(mgr.Manager),
	}))
}

func recoveryIntegrationSeedLegacy(t *testing.T, mgr *testutil.Manager) (*apiv1.Synthesizer, *apiv1.Composition) {
	t.Helper()
	syn, comp := writeGenericComposition(t, mgr.GetClient())
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	require.NoError(t, mgr.GetClient().Update(t.Context(), comp))
	// An old timestamp avoids the production grace period for newly-published slices.
	when := metav1.NewTime(time.Now().Add(-time.Minute))
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID: "legacy-current", Initialized: &when, Synthesized: &when,
		ObservedCompositionGeneration: comp.Generation,
		ObservedSynthesizerGeneration: syn.Generation,
		TombstoneRecoveryRequired:     false,
	}
	require.NoError(t, mgr.GetClient().Status().Update(t.Context(), comp))
	return syn, comp
}

func recoveryIntegrationCreateSlice(t *testing.T, mgr *testutil.Manager, comp *apiv1.Composition, name string, objects ...*unstructured.Unstructured) *apiv1.ResourceSlice {
	t.Helper()
	slice := &apiv1.ResourceSlice{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: comp.Namespace, Finalizers: []string{"eno.azure.io/cleanup"},
	}}
	require.NoError(t, controllerutil.SetControllerReference(comp, slice, mgr.GetScheme()))
	slice.Spec.SynthesisUUID = comp.Status.CurrentSynthesis.UUID
	for _, obj := range objects {
		raw, err := obj.MarshalJSON()
		require.NoError(t, err)
		slice.Spec.Resources = append(slice.Spec.Resources, apiv1.Manifest{Manifest: string(raw)})
	}
	require.NoError(t, mgr.GetClient().Create(t.Context(), slice))
	return slice
}

func recoveryIntegrationLoseSlice(t *testing.T, mgr *testutil.Manager, key client.ObjectKey) {
	t.Helper()
	slice := &apiv1.ResourceSlice{}
	require.NoError(t, mgr.GetAPIReader().Get(t.Context(), key, slice))
	require.NoError(t, mgr.GetClient().Delete(t.Context(), slice))
	require.NoError(t, retry.RetryOnConflict(testutil.Backoff, func() error {
		fresh := &apiv1.ResourceSlice{}
		if err := mgr.GetAPIReader().Get(t.Context(), key, fresh); err != nil {
			return client.IgnoreNotFound(err)
		}
		fresh.Finalizers = nil
		return client.IgnoreNotFound(mgr.GetClient().Update(t.Context(), fresh))
	}))
	testutil.Eventually(t, func() bool {
		return apierrors.IsNotFound(mgr.GetAPIReader().Get(t.Context(), key, &apiv1.ResourceSlice{}))
	})
}

func recoveryIntegrationConfigMap(name string, data map[string]string) *unstructured.Unstructured {
	values := make(map[string]any, len(data))
	for key, value := range data {
		values[key] = value
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": name, "namespace": "default"},
		"data":     values,
	}}
}

func recoveryIntegrationReadComposition(t *testing.T, mgr *testutil.Manager, key client.ObjectKey) *apiv1.Composition {
	t.Helper()
	comp := &apiv1.Composition{}
	require.NoError(t, mgr.GetAPIReader().Get(t.Context(), key, comp))
	return comp
}

func recoveryIntegrationWaitReady(t *testing.T, mgr *testutil.Manager, key client.ObjectKey, notUUID string) *apiv1.Composition {
	t.Helper()
	var stored *apiv1.Composition
	testutil.Eventually(t, func() bool {
		stored = &apiv1.Composition{}
		if err := mgr.GetAPIReader().Get(t.Context(), key, stored); err != nil {
			return false
		}
		syn := stored.Status.CurrentSynthesis
		return syn != nil && syn.UUID != notUUID &&
			syn.ObservedCompositionGeneration == stored.Generation &&
			syn.Synthesized != nil && syn.Reconciled != nil && syn.Ready != nil &&
			stored.Status.InFlightSynthesis == nil && !stored.ShouldForceResynthesis() &&
			stored.Status.Simplified != nil && stored.Status.Simplified.Status == "Ready"
	})
	return stored
}

func recoveryIntegrationAssertInventory(t *testing.T, mgr *testutil.Manager, comp *apiv1.Composition, expected map[string]bool) {
	t.Helper()
	inventory := map[string]bool{}
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		require.NotNil(t, ref)
		require.NotEmpty(t, ref.Name)
		slice := &apiv1.ResourceSlice{}
		require.NoError(t, mgr.GetAPIReader().Get(t.Context(), client.ObjectKey{Namespace: comp.Namespace, Name: ref.Name}, slice))
		require.Len(t, slice.Status.Resources, len(slice.Spec.Resources))
		for i, manifest := range slice.Spec.Resources {
			obj := &unstructured.Unstructured{}
			require.NoError(t, obj.UnmarshalJSON([]byte(manifest.Manifest)))
			require.Equal(t, "ConfigMap", obj.GetKind())
			require.Equal(t, "default", obj.GetNamespace())
			require.NotContains(t, inventory, obj.GetName())
			inventory[obj.GetName()] = manifest.Deleted
			require.True(t, slice.Status.Resources[i].Reconciled, obj.GetName())
			require.NotNil(t, slice.Status.Resources[i].Ready, obj.GetName())
			require.Equal(t, manifest.Deleted, slice.Status.Resources[i].Deleted, obj.GetName())
		}
	}
	require.Equal(t, expected, inventory)
}

func recoveryIntegrationAssertConfigMap(t *testing.T, mgr *testutil.Manager, name string, data map[string]string) {
	t.Helper()
	testutil.Eventually(t, func() bool {
		cm := &corev1.ConfigMap{}
		if err := mgr.DownstreamClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: name}, cm); err != nil {
			return false
		}
		if len(cm.Data) != len(data) {
			return false
		}
		for key, value := range data {
			if cm.Data[key] != value {
				return false
			}
		}
		return true
	})
}

func recoveryIntegrationAssertAbsent(t *testing.T, mgr *testutil.Manager, name string) {
	t.Helper()
	testutil.Eventually(t, func() bool {
		return apierrors.IsNotFound(mgr.DownstreamClient.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: name}, &corev1.ConfigMap{}))
	})
}
