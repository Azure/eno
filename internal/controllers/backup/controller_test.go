package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	enocel "github.com/Azure/eno/internal/cel"
	"github.com/Azure/eno/internal/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const controllerTestAddonFilter = `has(composition.metadata.labels) && composition.metadata.labels != null && 'aks.azure.com/component-type' in composition.metadata.labels ? composition.metadata.labels['aks.azure.com/component-type'] == 'addon' && (!has(self.metadata.labels) || self.metadata.labels == null || !('eno.azure.io/overlaymgr-component-type' in self.metadata.labels)) : has(self.metadata.labels) && self.metadata.labels != null && 'eno.azure.io/overlaymgr-component-type' in self.metadata.labels && self.metadata.labels['eno.azure.io/overlaymgr-component-type'] == 'addon'`

type controllerTestFixture struct {
	t          *testing.T
	upstream   client.WithWatch
	downstream client.WithWatch
	controller *backupController
	key        types.NamespacedName
}

func controllerTestUUID(sequence int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", sequence)
}

func controllerTestResource(name string) inventoryResource {
	return inventoryResource{Version: "v1", Kind: "ConfigMap", Namespace: "workloads", Name: name}
}

func newControllerTestFixture(t *testing.T, required bool, names ...string) *controllerTestFixture {
	t.Helper()
	comp := inventoryTestComposition()
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Status.CurrentSynthesis.UUID = controllerTestUUID(2)
	comp.Status.CurrentSynthesis.TombstoneRecoveryRequired = required
	comp.Status.CurrentSynthesis.ResourceSlices = []*apiv1.ResourceSliceRef{{Name: "desired"}}
	slice := &apiv1.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "desired", Namespace: comp.Namespace, UID: "desired-uid",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(comp, apiv1.SchemeGroupVersion.WithKind("Composition"))},
		},
		Spec: apiv1.ResourceSliceSpec{SynthesisUUID: comp.Status.CurrentSynthesis.UUID},
	}
	for _, name := range names {
		slice.Spec.Resources = append(slice.Spec.Resources, inventoryTestManifest(t, controllerTestResource(name)))
	}
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	f := &controllerTestFixture{
		t: t, key: client.ObjectKeyFromObject(comp),
		upstream: fake.NewClientBuilder().WithScheme(scheme).
			WithStatusSubresource(&apiv1.Composition{}, &apiv1.ResourceSlice{}).WithObjects(comp, slice).Build(),
		downstream: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}
	f.controller = &backupController{
		client: f.upstream, reader: f.upstream, downstream: f.downstream,
	}
	return f
}

func (f *controllerTestFixture) composition() *apiv1.Composition {
	f.t.Helper()
	comp := &apiv1.Composition{}
	require.NoError(f.t, f.upstream.Get(f.t.Context(), f.key, comp))
	return comp
}

func (f *controllerTestFixture) updateStatus(change func(*apiv1.Synthesis)) {
	f.t.Helper()
	comp := f.composition()
	change(comp.Status.CurrentSynthesis)
	require.NoError(f.t, f.upstream.Status().Update(f.t.Context(), comp))
}

func (f *controllerTestFixture) reconcile() error {
	f.t.Helper()
	_, err := f.controller.Reconcile(f.t.Context(), ctrl.Request{NamespacedName: f.key})
	return err
}

func (f *controllerTestFixture) reconcileEvent() {
	f.t.Helper()
	result, err := f.controller.Reconcile(f.t.Context(), ctrl.Request{NamespacedName: f.key})
	require.NoError(f.t, err)
	require.Equal(f.t, ctrl.Result{}, result, "successful transitions must rely on watch events, not explicit requeues")
}

func (f *controllerTestFixture) finish(reason string) {
	f.t.Helper()
	for range 12 {
		require.NoError(f.t, f.reconcile())
		syn := f.composition().Status.CurrentSynthesis
		if done := syn.TombstoneRecoveryFinished; done != nil && done.Status {
			require.Equal(f.t, syn.UUID, done.SynthesisUUID)
			require.Equal(f.t, reason, done.Reason)
			return
		}
	}
	f.t.Fatal("recovery did not reach a terminal decision")
}

func (f *controllerTestFixture) expectError(want error) {
	f.t.Helper()
	for range 12 {
		if err := f.reconcile(); err != nil {
			require.ErrorIs(f.t, err, want)
			return
		}
	}
	f.t.Fatal("the injected API failure was not surfaced")
}

func (f *controllerTestFixture) ready() {
	f.t.Helper()
	// Readiness comes from resource reconciliation, not from the backup controller.
	f.updateStatus(func(syn *apiv1.Synthesis) {
		ready := metav1.NewTime(syn.Synthesized.Add(time.Second))
		syn.Ready = &ready
	})
}

func (f *controllerTestFixture) inventories() []corev1.ConfigMap {
	f.t.Helper()
	list := &corev1.ConfigMapList{}
	require.NoError(f.t, f.downstream.List(f.t.Context(), list, client.InNamespace("kube-system")))
	return list.Items
}

func (f *controllerTestFixture) assertInventory(sequence int, names ...string) {
	f.t.Helper()
	items := f.inventories()
	require.Len(f.t, items, 1)
	data, err := decodeInventorySnapshot(f.composition(), items[0])
	require.NoError(f.t, err)
	assert.Equal(f.t, controllerTestUUID(sequence), items[0].Annotations[inventorySynthesisUUIDAnnotation])
	want := []inventoryResource{}
	for _, name := range names {
		want = append(want, controllerTestResource(name))
	}
	assert.ElementsMatch(f.t, want, data)
}

func (f *controllerTestFixture) history(names ...string) *corev1.ConfigMap {
	f.t.Helper()
	comp := f.composition()
	comp.Status.CurrentSynthesis.UUID = controllerTestUUID(1)
	synthesized := metav1.NewTime(comp.Status.CurrentSynthesis.Synthesized.Add(-time.Minute))
	comp.Status.CurrentSynthesis.Synthesized = &synthesized
	slice := apiv1.ResourceSlice{}
	for _, name := range names {
		slice.Spec.Resources = append(slice.Spec.Resources, inventoryTestManifest(f.t, controllerTestResource(name)))
	}
	item, err := makeInventory(comp, []apiv1.ResourceSlice{slice})
	require.NoError(f.t, err)
	item.UID = "history-uid"
	require.NoError(f.t, f.downstream.Create(f.t.Context(), item))
	return item
}

func (f *controllerTestFixture) slice(name string) *apiv1.ResourceSlice {
	f.t.Helper()
	slice := &apiv1.ResourceSlice{}
	require.NoError(f.t, f.upstream.Get(f.t.Context(), types.NamespacedName{Namespace: f.key.Namespace, Name: name}, slice))
	return slice
}

func (f *controllerTestFixture) restart() {
	f.t.Helper()
	old := f.controller
	f.controller = &backupController{
		client: old.client, reader: old.reader, downstream: old.downstream,
		resourceFilter: old.resourceFilter,
	}
}

func TestBackupControllerEventDrivenPhases(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		required bool
		history  bool
	}{
		{reason: reasonNotNeeded, history: true},
		{reason: reasonInventoryNotFound, required: true},
		{reason: reasonFinished, required: true, history: true},
	} {
		for _, initiallyReady := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/initially-ready-%t", tc.reason, initiallyReady), func(t *testing.T) {
				f := newControllerTestFixture(t, tc.required, "desired")
				if tc.history {
					f.history("desired")
				}
				if initiallyReady {
					f.ready()
				}
				before := f.inventories()
				if !tc.required {
					// NotNeeded must not read downstream history.
					f.controller.downstream = nil
				}
				f.reconcileEvent()
				status := f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished
				require.NotNil(t, status)
				assert.Equal(t, controllerTestUUID(2), status.SynthesisUUID)
				assert.True(t, status.Status)
				assert.Equal(t, before, f.inventories(), "initial decision must not record inventory, even when Ready")
				f.controller.downstream = f.downstream
				syn := f.composition().Status.CurrentSynthesis
				require.True(t, syn.TombstoneRecoveryComplete())
				require.Equal(t, tc.reason, syn.TombstoneRecoveryFinished.Reason)
				assert.Equal(t, tc.required, syn.TombstoneRecoveryRequired)
				assert.Equal(t, before, f.inventories(), "finishing recovery must not record inventory on the same pass")
				if !initiallyReady {
					f.reconcileEvent()
					assert.Equal(t, before, f.inventories())
					f.updateStatus(func(syn *apiv1.Synthesis) {
						reconciled := metav1.NewTime(syn.Synthesized.Add(time.Second))
						syn.Reconciled = &reconciled
					})
					f.reconcileEvent()
					assert.Equal(t, before, f.inventories(), "Reconciled alone must not trigger inventory recording")
					f.ready()
				}
				beforeBackup := f.composition()
				f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
					SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
						t.Error("inventory update must not patch Composition status")
						return fmt.Errorf("unexpected status patch")
					},
				})
				f.reconcileEvent()
				f.assertInventory(2, "desired")
				assert.Equal(t, beforeBackup, f.composition())
			})
		}
	}
}

func TestBackupControllerBaseline(t *testing.T) {
	for _, names := range [][]string{{"desired"}, {}} {
		t.Run(fmt.Sprintf("resources-%d", len(names)), func(t *testing.T) {
			f := newControllerTestFixture(t, true, names...)
			f.finish("InventoryNotFound")
			require.NoError(t, f.reconcile())
			require.Empty(t, f.inventories(), "inventory must wait for Ready")
			f.ready()
			require.NoError(t, f.reconcile())
			f.assertInventory(2, names...)
			assert.True(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryRequired,
				"a new baseline does not prove that unknown historical resources were recovered")
			before := f.inventories()
			require.NoError(t, f.reconcile())
			assert.Equal(t, before, f.inventories(), "repeated events must not rewrite an already persisted snapshot")
			f.restart()
			require.NoError(t, f.reconcile())
			assert.Equal(t, before, f.inventories(), "restarting must not rewrite an already persisted snapshot")
		})
	}
}

func TestBackupControllerRecoveryRetry(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow-%t", overflow), func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			f.history("desired", "removed")
			original := f.slice("desired")
			if overflow {
				var obj map[string]any
				require.NoError(t, json.Unmarshal([]byte(original.Spec.Resources[0].Manifest), &obj))
				obj["data"] = map[string]string{"padding": ""}
				size := len(inventoryTestJSON(t, obj))
				obj["data"] = map[string]string{"padding": strings.Repeat("x", resource.MaxSliceJSONBytes-size)}
				original.Spec.Resources[0].Manifest = inventoryTestJSON(t, obj)
				require.Len(t, original.Spec.Resources[0].Manifest, resource.MaxSliceJSONBytes)
				require.NoError(t, f.upstream.Update(t.Context(), original))
			}
			before := original.Spec.Resources[0]
			rejected := false
			rejection := apierrors.NewConflict(schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "compositions"}, f.key.Name, fmt.Errorf("status write rejected"))
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, cli client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					comp := obj.(*apiv1.Composition)
					status := comp.Status.CurrentSynthesis.TombstoneRecoveryFinished
					if status != nil && status.Status && status.Reason == "FinishedTombstoneRecovery" && !rejected {
						rejected = true
						return rejection
					}
					return cli.SubResource(name).Patch(ctx, obj, patch, opts...)
				},
			})
			f.expectError(rejection)
			assert.False(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryComplete())
			f.restart()
			f.finish("FinishedTombstoneRecovery")
			syn := f.composition().Status.CurrentSynthesis
			require.Equal(t, before, f.slice("desired").Spec.Resources[0], "existing manifest and index must not change")
			wantSlices := 1
			if overflow {
				wantSlices = 2
			}
			require.Len(t, syn.ResourceSlices, wantSlices)
			all := &apiv1.ResourceSliceList{}
			require.NoError(t, f.upstream.List(t.Context(), all))
			require.Len(t, all.Items, wantSlices, "a retry must not leak duplicate overflow slices")
			var tombstones []apiv1.Manifest
			for _, ref := range syn.ResourceSlices {
				slice := f.slice(ref.Name)
				size := 0
				for _, manifest := range slice.Spec.Resources {
					size += len(manifest.Manifest)
					if manifest.Deleted {
						tombstones = append(tombstones, manifest)
					}
				}
				assert.LessOrEqual(t, size, resource.MaxSliceJSONBytes)
			}
			require.Len(t, tombstones, 1)
			assert.JSONEq(t, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"removed","namespace":"workloads"}}`, tombstones[0].Manifest)
			require.Len(t, f.inventories(), 1, "old inventory must survive until replacement is ready")
			f.ready()
			require.NoError(t, f.reconcile())
			f.assertInventory(2, "desired")
			assert.True(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryRequired)
		})
	}
}

func TestBackupControllerRotation(t *testing.T) {
	for _, equalTime := range []bool{false, true} {
		t.Run(fmt.Sprintf("equal-timestamps-%t", equalTime), func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			old := f.history("desired")
			if equalTime {
				old.Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Format(time.RFC3339)
				require.NoError(t, f.downstream.Update(t.Context(), old))
			}
			var events []string
			lists := 0
			expectedNew := ""
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				List: func(ctx context.Context, cli client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
					lists++
					return cli.List(ctx, obj, opts...)
				},
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					err := cli.Create(ctx, obj, opts...)
					if err == nil {
						events = append(events, "created:"+obj.GetName())
					}
					return err
				},
				Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					require.NoError(t, cli.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: expectedNew}, &corev1.ConfigMap{}),
						"the replacement must exist before deleting history")
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.NotNil(t, options.Preconditions)
					require.NotNil(t, options.Preconditions.UID)
					require.NotNil(t, options.Preconditions.ResourceVersion)
					require.Equal(t, obj.GetUID(), *options.Preconditions.UID)
					require.Equal(t, obj.GetResourceVersion(), *options.Preconditions.ResourceVersion)
					err := cli.Delete(ctx, obj, opts...)
					if err == nil {
						events = append(events, "deleted:"+obj.GetName())
					}
					return err
				},
			})
			for _, sequence := range []int{2, 3} {
				if sequence == 3 {
					comp := f.composition()
					comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis.DeepCopy()
					slice := f.slice("desired").DeepCopy()
					slice.Name, slice.UID, slice.ResourceVersion = "desired-next", "", ""
					slice.Spec.SynthesisUUID = controllerTestUUID(sequence)
					require.NoError(t, f.upstream.Create(t.Context(), slice))
					when := metav1.NewTime(comp.Status.CurrentSynthesis.Synthesized.Add(time.Minute))
					if equalTime {
						when = *comp.Status.CurrentSynthesis.Synthesized
					}
					comp.Status.CurrentSynthesis = &apiv1.Synthesis{
						UUID: controllerTestUUID(sequence), Synthesized: &when,
						ResourceSlices: []*apiv1.ResourceSliceRef{{Name: slice.Name}},
					}
					require.NoError(t, f.upstream.Status().Update(t.Context(), comp))
				}
				beforeLists := lists
				f.finish("NotNeeded")
				assert.Equal(t, beforeLists, lists, "NotNeeded preparation must not discover inventory")
				expectedNew = inventoryName(f.composition(), controllerTestUUID(sequence))
				f.ready()
				require.NoError(t, f.reconcile())
				f.assertInventory(sequence, "desired")
				require.Equal(t, []string{"created:" + expectedNew, "deleted:" + old.Name}, events)
				events = nil
				item := f.inventories()[0]
				old = &item
			}
			f.restart()
			require.NoError(t, f.reconcile())
			f.assertInventory(3, "desired")
			assert.Empty(t, events)
		})
	}
}

func TestBackupControllerReplacesNewerInventory(t *testing.T) {
	f := newControllerTestFixture(t, false, "desired")
	newer := f.history("desired")
	newer.Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Add(time.Minute).Format(time.RFC3339)
	require.NoError(t, f.downstream.Update(t.Context(), newer))
	f.finish(reasonNotNeeded)
	f.ready()

	require.NoError(t, f.reconcile())
	require.Len(t, f.inventories(), 1)
	persisted := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: inventoryNamespace, Name: inventoryName(f.composition(), controllerTestUUID(2))}
	require.NoError(t, f.downstream.Get(t.Context(), key, persisted))
	resources, err := decodeInventorySnapshot(f.composition(), *persisted)
	require.NoError(t, err)
	assert.Equal(t, []inventoryResource{controllerTestResource("desired")}, resources)
	require.True(t, apierrors.IsNotFound(f.downstream.Get(t.Context(), client.ObjectKeyFromObject(newer), &corev1.ConfigMap{})))
}

func TestBackupControllerReplacesMalformedHistory(t *testing.T) {
	for _, timestamp := range []string{"invalid", "", "0001-01-01T00:00:00Z"} {
		t.Run(timestamp, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			old := f.history("removed")
			old.Annotations[inventorySynthesizedAnnotation] = timestamp
			old.Annotations[inventoryCompositionNamespaceAnnotation] = "untrusted-history"
			old.Data[inventoryDataKey] = "{"
			require.NoError(t, f.downstream.Update(t.Context(), old))
			f.finish(reasonNotNeeded)
			f.ready()
			before := f.composition()
			f.reconcileEvent()
			f.assertInventory(2, "desired")
			assert.Equal(t, before, f.composition())
			assert.Len(t, f.slice("desired").Spec.Resources, 1, "replacement must not compute historical tombstones")
		})
	}
}

func TestBackupControllerOutageRecovery(t *testing.T) {
	f := newControllerTestFixture(t, true, "desired")
	old := f.history("desired", "removed")
	f.ready()
	unavailable := apierrors.NewServiceUnavailable("downstream API is starting")
	offline := true
	f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
		List: func(ctx context.Context, cli client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
			if offline {
				return unavailable
			}
			return cli.List(ctx, obj, opts...)
		},
	})
	for range 5 {
		f.expectError(unavailable)
		syn := f.composition().Status.CurrentSynthesis
		require.NotNil(t, syn.TombstoneRecoveryFinished)
		assert.False(t, syn.TombstoneRecoveryFinished.Status)
		assert.Equal(t, "InventoryGetError", syn.TombstoneRecoveryFinished.Reason)
		assert.Zero(t, syn.TombstoneRecoveryFinished.InventoryAttempts)
		require.Equal(t, []corev1.ConfigMap{*old}, f.inventories())
	}
	offline = false
	f.finish("FinishedTombstoneRecovery")
	assert.Equal(t, []corev1.ConfigMap{*old}, f.inventories(), "premature Ready must not record inventory during recovery")
	f.reconcileEvent()
	f.assertInventory(2, "desired")
}

func TestBackupControllerRotationFailures(t *testing.T) {
	for _, operation := range []string{"create", "delete"} {
		t.Run(operation, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			old := f.history("desired")
			f.finish("NotNeeded")
			f.ready()
			failure := apierrors.NewServiceUnavailable("temporary inventory write failure")
			injected := false
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if operation == "create" && !injected {
						injected = true
						return failure
					}
					return cli.Create(ctx, obj, opts...)
				},
				Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if operation == "delete" && !injected {
						injected = true
						return failure
					}
					return cli.Delete(ctx, obj, opts...)
				},
			})
			f.expectError(failure)
			require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(old), &corev1.ConfigMap{}))
			if operation == "create" {
				require.Len(t, f.inventories(), 1, "failed replacement must leave the old inventory intact")
			} else {
				require.Len(t, f.inventories(), 2, "failed cleanup must preserve the persisted replacement too")
			}
			f.restart()
			require.NoError(t, f.reconcile())
			f.assertInventory(2, "desired")
		})
	}
}

func TestBackupControllerInventoryUpdateHonorsRecoveryDecision(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid-%t", invalid), func(t *testing.T) {
			f := newControllerTestFixture(t, invalid, "desired")
			old := f.history("desired", "removed")
			reason := "NotNeeded"
			if invalid {
				old.Data["inventory.json"] = "{"
				require.NoError(t, f.downstream.Update(t.Context(), old))
				reason = "InventoryInvalid"
			}
			f.finish(reason)
			f.ready()
			for range 2 {
				require.NoError(t, f.reconcile())
			}
			if invalid {
				assert.Equal(t, []corev1.ConfigMap{*old}, f.inventories(),
					"inventory rejected during recovery must not be replaced or deleted")
			} else {
				f.assertInventory(2, "desired")
				assert.True(t, apierrors.IsNotFound(f.downstream.Get(t.Context(), client.ObjectKeyFromObject(old), &corev1.ConfigMap{})),
					"inventory update must rotate history without repeating the tombstone diff")
			}
		})
	}
}

func TestBackupControllerDisabled(t *testing.T) {
	require.NoError(t, NewController(nil, Options{Enabled: false}), "disabled backup must not access the manager or register controllers")
}

func TestBackupControllerNamespaceRequired(t *testing.T) {
	require.EqualError(t, NewController(nil, Options{Enabled: true}), "backup namespace is required")
}

func TestBackupControllerResourceFilterSkipsWrites(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprintf("recovery-completed-%t", completed), func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			if completed {
				f.finish(reasonInventoryNotFound)
				f.ready()
			}
			filter, err := enocel.Parse(`composition.metadata.labels.owner == "other"`)
			require.NoError(t, err)
			f.controller.resourceFilter = filter
			before := f.composition()
			slice := f.slice("desired")
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					t.Error("filtered Composition must not receive status writes")
					return fmt.Errorf("unexpected status patch")
				},
			})
			// Any downstream operation is a test failure, including discovery.
			f.controller.reader = nil
			f.controller.downstream = nil
			f.reconcileEvent()
			after := f.composition()
			assert.Equal(t, before, after)
			assert.Equal(t, slice, f.slice("desired"))
			assert.Empty(t, f.inventories())
		})
	}
}

func TestBackupControllerAddonResourceFilter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		componentType string
		overlayType   string
		matches       bool
	}{
		{name: "addon", componentType: "addon", matches: true},
		{name: "ccp", componentType: "ccp"},
		{name: "legacy-addon", overlayType: "addon"},
		{name: "legacy-ccp", overlayType: "ccp"},
		{name: "legacy-unlabeled"},
		{name: "addon-ignores-resource-labels", componentType: "addon", overlayType: "addon", matches: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			comp := f.composition()
			comp.Labels = map[string]string{}
			if tc.componentType != "" {
				comp.Labels["aks.azure.com/component-type"] = tc.componentType
			}
			require.NoError(t, f.upstream.Update(t.Context(), comp))
			slice := f.slice("desired")
			res := controllerTestResource("desired")
			if tc.overlayType != "" {
				res.Labels = map[string]string{"eno.azure.io/overlaymgr-component-type": tc.overlayType}
			}
			slice.Spec.Resources[0] = inventoryTestManifest(t, res)
			require.NoError(t, f.upstream.Update(t.Context(), slice))
			var err error
			f.controller.resourceFilter, err = enocel.Parse(controllerTestAddonFilter)
			require.NoError(t, err)
			f.controller.reader = nil
			f.controller.downstream = nil
			statusWrites := 0
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, cli client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					statusWrites++
					return cli.SubResource(name).Patch(ctx, obj, patch, opts...)
				},
			})
			before := f.composition()
			require.NoError(t, f.reconcile())
			if tc.matches {
				require.Equal(t, 1, statusWrites)
				status := f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished
				require.NotNil(t, status)
				assert.True(t, status.Status)
				assert.Equal(t, reasonNotNeeded, status.Reason)
			} else {
				assert.Zero(t, statusWrites)
				assert.Equal(t, before, f.composition())
			}
			assert.Equal(t, slice.Spec, f.slice("desired").Spec)
			assert.Empty(t, f.inventories())
		})
	}
}

func TestBackupControllerEmptyCurrentResourceFilter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		componentType string
		inventoryType string
		matches       bool
	}{
		{name: "inventory-addon", inventoryType: "addon"},
		{name: "inventory-ccp", inventoryType: "ccp"},
		{name: "empty-addon", componentType: "addon", matches: true},
		{name: "empty-addon-recovers-history", componentType: "addon", inventoryType: "addon", matches: true},
		{name: "empty-ccp", componentType: "ccp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerTestFixture(t, true)
			comp := f.composition()
			comp.Labels = nil
			if tc.componentType != "" {
				comp.Labels = map[string]string{"aks.azure.com/component-type": tc.componentType}
			}
			require.NoError(t, f.upstream.Update(t.Context(), comp))
			f.updateStatus(func(syn *apiv1.Synthesis) { syn.ResourceSlices = nil })
			if tc.inventoryType != "" {
				item := f.history("removed")
				res := controllerTestResource("removed")
				res.Labels = map[string]string{"eno.azure.io/overlaymgr-component-type": tc.inventoryType}
				item.Data[inventoryDataKey] = inventoryTestJSON(t, []inventoryResource{res})
				require.NoError(t, f.downstream.Update(t.Context(), item))
			}
			var err error
			f.controller.resourceFilter, err = enocel.Parse(controllerTestAddonFilter)
			require.NoError(t, err)
			before, history := f.composition(), f.inventories()
			if tc.matches {
				reason := reasonInventoryNotFound
				if tc.inventoryType != "" {
					reason = reasonFinished
				}
				f.finish(reason)
				if tc.inventoryType != "" {
					syn := f.composition().Status.CurrentSynthesis
					require.Len(t, syn.ResourceSlices, 1)
					slice := f.slice(syn.ResourceSlices[0].Name)
					require.Len(t, slice.Spec.Resources, 1)
					assert.True(t, slice.Spec.Resources[0].Deleted)
					_, res, err := parseInventoryManifest(slice.Spec.Resources[0].Manifest)
					require.NoError(t, err)
					assert.Equal(t, "addon", res.Labels["eno.azure.io/overlaymgr-component-type"])
				}
			} else {
				f.controller.reader = nil
				f.controller.downstream = nil
				f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
					SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
						t.Error("filtered Composition must not receive status writes")
						return fmt.Errorf("unexpected status patch")
					},
				})
				require.NoError(t, f.reconcile())
				assert.Equal(t, before, f.composition())
			}
			assert.Equal(t, history, f.inventories(), "routing and recovery must not rewrite inventory")
		})
	}
}

func TestBackupControllerDeletingSkipsInventory(t *testing.T) {
	for _, recording := range []bool{false, true} {
		t.Run(fmt.Sprintf("recording-%t", recording), func(t *testing.T) {
			f := newControllerTestFixture(t, !recording)
			history := f.history("removed")
			if recording {
				f.finish(reasonNotNeeded)
				f.ready()
			}
			f.updateStatus(func(syn *apiv1.Synthesis) { syn.ResourceSlices = nil })
			var err error
			f.controller.resourceFilter, err = enocel.Parse(controllerTestAddonFilter)
			require.NoError(t, err)
			require.NoError(t, f.upstream.Delete(t.Context(), f.composition()))
			before := f.composition()
			f.controller.downstream = nil
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					t.Error("deleting Composition must not receive backup status writes")
					return fmt.Errorf("unexpected status patch")
				},
			})
			require.NoError(t, f.reconcile())
			assert.Equal(t, before, f.composition())
			assert.Equal(t, []corev1.ConfigMap{*history}, f.inventories())
		})
	}
}

func TestBackupControllerRecordingSupersededAfterCreate(t *testing.T) {
	f := newControllerTestFixture(t, false, "desired")
	old := f.history("desired")
	f.finish(reasonNotNeeded)
	f.ready()
	var expected *apiv1.Composition
	f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
		Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := cli.Create(ctx, obj, opts...); err != nil {
				return err
			}
			f.updateStatus(func(syn *apiv1.Synthesis) {
				syn.UUID = controllerTestUUID(3)
				syn.TombstoneRecoveryFinished = nil
			})
			expected = f.composition()
			return nil
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			t.Error("superseded recording must not delete history")
			return fmt.Errorf("unexpected inventory delete")
		},
	})
	result, err := f.controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{Requeue: true}, result)
	require.NotNil(t, expected)
	assert.Equal(t, expected, f.composition())
	assert.Len(t, f.inventories(), 2)
	require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(old), &corev1.ConfigMap{}))
}

func TestBackupControllerObsoleteWork(t *testing.T) {
	for _, mode := range []string{"already-deleting", "deletion-during-read", "new-synthesis"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			old := f.history("desired", "removed")
			original := f.slice("desired")
			var expected *apiv1.Composition
			if mode == "already-deleting" {
				require.NoError(t, f.upstream.Delete(t.Context(), f.composition()))
				expected = f.composition()
				f.controller.downstream = nil
			} else {
				f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
					List: func(ctx context.Context, cli client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
						err := cli.List(ctx, obj, opts...)
						if err != nil || expected != nil {
							return err
						}
						if mode == "deletion-during-read" {
							require.NoError(t, f.upstream.Delete(ctx, f.composition()))
						} else {
							f.updateStatus(func(syn *apiv1.Synthesis) {
								syn.UUID = controllerTestUUID(3)
								syn.TombstoneRecoveryFinished = nil
							})
						}
						expected = f.composition()
						return nil
					},
				})
			}
			for range 12 {
				result, err := f.controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
				require.NoError(t, err)
				if expected != nil {
					assert.Equal(t, ctrl.Result{Requeue: mode != "already-deleting"}, result)
					break
				}
				assert.Equal(t, ctrl.Result{}, result)
			}

			require.NotNil(t, expected, "the external deletion or supersession must occur")
			assert.Equal(t, expected, f.composition(), "obsolete recovery must not acknowledge or change the new state")
			assert.Equal(t, original.Spec, f.slice("desired").Spec)
			assert.Equal(t, []corev1.ConfigMap{*old}, f.inventories())
			if mode != "new-synthesis" {
				f.restart()
				f.controller.downstream = nil
				require.NoError(t, f.reconcile())
				assert.Equal(t, expected, f.composition())
			}
		})
	}
}

func TestBackupControllerStatusWriteRace(t *testing.T) {
	for _, mode := range []string{"deletion", "supersession"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			var expected *apiv1.Composition
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, cli client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					if expected == nil {
						if mode == "deletion" {
							require.NoError(t, f.upstream.Delete(ctx, f.composition()))
						} else {
							f.updateStatus(func(syn *apiv1.Synthesis) { syn.UUID = controllerTestUUID(3) })
						}

						expected = f.composition()
					}
					// Apply the real JSON patch against the changed object, rather than injecting its rejection.
					return cli.SubResource(name).Patch(ctx, obj, patch, opts...)
				},
			})
			require.Error(t, f.reconcile(), "stale recovery status must not be accepted")
			require.NotNil(t, expected)
			assert.Equal(t, expected, f.composition())
			assert.Nil(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished)
			assert.Empty(t, f.inventories())
		})
	}
}

func TestBackupControllerRecoveryInventorySelection(t *testing.T) {
	for _, invalidTime := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid-time-%t", invalidTime), func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			older := f.history("obsolete")
			newer := older.DeepCopy()
			newer.Name = inventoryName(f.composition(), controllerTestUUID(3))
			newer.UID, newer.ResourceVersion = "newer-uid", ""
			newer.Annotations[inventorySynthesisUUIDAnnotation] = controllerTestUUID(3)
			newer.Annotations[inventorySynthesizedAnnotation] = f.composition().Status.CurrentSynthesis.Synthesized.Format(time.RFC3339)
			newer.Data[inventoryDataKey] = inventoryTestJSON(t, []inventoryResource{controllerTestResource("removed")})
			require.NoError(t, f.downstream.Create(t.Context(), newer))
			older.Data[inventoryDataKey] = "{"
			if invalidTime {
				older.Annotations[inventorySynthesizedAnnotation] = "invalid"
			}
			require.NoError(t, f.downstream.Update(t.Context(), older))
			f.reconcileEvent()
			status := f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished
			require.NotNil(t, status)
			require.True(t, status.Status)
			if invalidTime {
				assert.Equal(t, reasonInventoryInvalid, status.Reason)
				assert.Contains(t, status.Message, "invalid source synthesized timestamp")
				assert.Len(t, f.slice("desired").Spec.Resources, 1)
				before := f.inventories()
				f.ready()
				f.controller.downstream = nil
				f.reconcileEvent()
				assert.Equal(t, before, f.inventories(), "an invalid recovery decision deliberately blocks replacement")
			} else {
				assert.Equal(t, reasonFinished, status.Reason)
				manifests := f.slice("desired").Spec.Resources
				require.Len(t, manifests, 2)
				assert.True(t, manifests[1].Deleted)
				_, got, err := parseInventoryManifest(manifests[1].Manifest)
				require.NoError(t, err)
				assert.Equal(t, controllerTestResource("removed"), got, "only the newest snapshot is decoded")
			}
		})
	}
}

func TestBackupControllerInventoryScopeAndAPIReads(t *testing.T) {
	f := newControllerTestFixture(t, false, "desired")
	old := f.history("removed")
	var unrelated []*corev1.ConfigMap
	for _, mode := range []string{"namespace", "lineage", "unlabeled"} {
		item := old.DeepCopy()
		item.ResourceVersion, item.UID = "", types.UID(mode)
		item.Data[inventoryDataKey] = "{"
		item.Annotations = nil
		switch mode {
		case "namespace":
			item.Namespace = "other"
		case "lineage":
			item.Name = "other-lineage"
			item.Labels[inventoryLineageLabel] = "other"
		case "unlabeled":
			item.Name = "unlabeled"
			item.Labels = nil
		}
		require.NoError(t, f.downstream.Create(t.Context(), item))
		unrelated = append(unrelated, item.DeepCopy())
	}
	f.finish(reasonNotNeeded)
	f.ready()
	before := f.composition()
	var events []string
	f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
		Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			require.IsType(t, &apiv1.Composition{}, obj)
			events = append(events, "cached-composition")
			return cli.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			t.Fatal("inventory must not publish status")
			return nil
		},
	})
	f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
		Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			switch obj.(type) {
			case *apiv1.Composition:
				events = append(events, "current-composition")
			case *apiv1.ResourceSlice:
				events = append(events, "slice")
			default:
				t.Fatalf("unexpected read %T", obj)
			}
			return cli.Get(ctx, key, obj, opts...)
		},
	})
	f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
		List: func(ctx context.Context, cli client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			options := (&client.ListOptions{}).ApplyOptions(opts)
			assert.Equal(t, inventoryNamespace, options.Namespace)
			assert.Equal(t, inventoryLineageLabel+"="+inventoryLineage(before), options.LabelSelector.String())
			events = append(events, "list")
			return cli.List(ctx, list, opts...)
		},
		Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			events = append(events, "create")
			return cli.Create(ctx, obj, opts...)
		},
		Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			events = append(events, "existing")
			return cli.Get(ctx, key, obj, opts...)
		},
		Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			events = append(events, "delete")
			return cli.Delete(ctx, obj, opts...)
		},
	})
	f.reconcileEvent()
	assert.Equal(t, []string{"cached-composition", "list", "slice", "current-composition", "create", "current-composition", "delete"}, events)
	events = nil
	f.reconcileEvent()
	assert.Equal(t, []string{"cached-composition", "list", "slice", "current-composition", "create", "existing"}, events)
	assert.Equal(t, before, f.composition())
	for _, item := range unrelated {
		got := &corev1.ConfigMap{}
		require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(item), got))
		assert.Equal(t, item, got)
	}
}

func TestBackupControllerInventoryPersistenceVerification(t *testing.T) {
	for _, mode := range []string{"matching", "different-resources", "different-time", "invalid-payload", "deleting", "get-failure", "mutated-create-response"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			old := f.history("removed")
			f.finish(reasonNotNeeded)
			f.ready()
			before := f.composition()
			intended, err := makeInventory(before, []apiv1.ResourceSlice{*f.slice("desired")})
			require.NoError(t, err)
			existing := intended.DeepCopy()
			switch mode {
			case "different-resources":
				existing.Data[inventoryDataKey] = "[]"
			case "different-time":
				existing.Annotations[inventorySynthesizedAnnotation] = "2026-09-16T12:01:00Z"
			case "invalid-payload":
				existing.Data[inventoryDataKey] = "{"
			case "deleting":
				existing.Finalizers = []string{"test.example/hold"}
			}
			if mode != "mutated-create-response" {
				require.NoError(t, f.downstream.Create(t.Context(), existing))
				if mode == "deleting" {
					require.NoError(t, f.downstream.Delete(t.Context(), existing))
					require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(existing), existing))
				}
			}
			failure := apierrors.NewServiceUnavailable("reading retry snapshot")
			creates, gets, deletes := 0, 0, 0
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					creates++
					if mode == "mutated-create-response" {
						obj.(*corev1.ConfigMap).Data[inventoryDataKey] = "[]"
					}
					return cli.Create(ctx, obj, opts...)
				},
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					gets++
					if mode == "get-failure" {
						return failure
					}
					return cli.Get(ctx, key, obj, opts...)
				},
				Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					return cli.Delete(ctx, obj, opts...)
				},
			})
			err = f.reconcile()
			if mode == "matching" {
				require.NoError(t, err)
				assert.Equal(t, 1, deletes)
				f.assertInventory(2, "desired")
			} else {
				require.Error(t, err)
				switch mode {
				case "get-failure":
					assert.ErrorIs(t, err, failure)
				case "invalid-payload":
					assert.ErrorContains(t, err, "decoding inventory.json")
				case "deleting":
					assert.ErrorContains(t, err, "configmap is being deleted")
				default:
					assert.ErrorContains(t, err, "does not match the intended snapshot")
				}
				assert.Zero(t, deletes)
				got := &corev1.ConfigMap{}
				require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(old), got))
				assert.Equal(t, old, got)
			}
			assert.Equal(t, 1, creates)
			if mode == "mutated-create-response" {
				assert.Zero(t, gets, "successful Create already returns the persisted snapshot")
			} else {
				assert.Equal(t, 1, gets)
				got := &corev1.ConfigMap{}
				require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(existing), got))
				assert.Equal(t, existing, got, "AlreadyExists must never overwrite the stored snapshot")
			}
			assert.Equal(t, before, f.composition(), "even failed inventory writes must not patch status")
		})
	}
}

func TestBackupControllerPartialInventoryCleanup(t *testing.T) {
	for _, mode := range []string{"delete-failure", "not-found", "new-synthesis", "not-ready", "deleting", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			first := f.history("old")
			second := first.DeepCopy()
			second.Name = inventoryName(f.composition(), controllerTestUUID(3))
			second.UID, second.ResourceVersion = "second-history", ""
			second.Annotations[inventorySynthesisUUIDAnnotation] = controllerTestUUID(3)
			require.NoError(t, f.downstream.Create(t.Context(), second))
			f.finish(reasonNotNeeded)
			f.ready()
			before := f.composition()
			failure := apierrors.NewServiceUnavailable("cleanup failure")
			deletes, reads := 0, 0
			f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*apiv1.Composition); ok {
						reads++
						if mode == "read-failure" && reads == 3 {
							return failure
						}
					}
					return cli.Get(ctx, key, obj, opts...)
				},
			})
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				Delete: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deletes++
					options := (&client.DeleteOptions{}).ApplyOptions(opts)
					require.NotNil(t, options.Preconditions)
					require.Equal(t, &obj.(*corev1.ConfigMap).UID, options.Preconditions.UID)
					require.Equal(t, &obj.(*corev1.ConfigMap).ResourceVersion, options.Preconditions.ResourceVersion)
					if deletes == 2 && mode == "delete-failure" {
						return failure
					}
					if deletes == 2 && mode == "not-found" {
						require.NoError(t, cli.Delete(ctx, obj))
					}
					err := cli.Delete(ctx, obj, opts...)
					if deletes == 1 {
						switch mode {
						case "new-synthesis":
							f.updateStatus(func(syn *apiv1.Synthesis) { syn.UUID = controllerTestUUID(4) })
						case "not-ready":
							f.updateStatus(func(syn *apiv1.Synthesis) { syn.Ready = nil })
						case "deleting":
							require.NoError(t, f.upstream.Delete(ctx, f.composition()))
						}
					}
					return err
				},
			})
			result, err := f.controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
			switch mode {
			case "delete-failure", "read-failure":
				require.ErrorIs(t, err, failure)
				assert.Equal(t, ctrl.Result{}, result)
			case "not-found":
				require.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result)
			default:
				require.NoError(t, err)
				assert.Equal(t, ctrl.Result{Requeue: true}, result)
			}
			assert.Equal(t, 3, reads, "freshness checks belong immediately before create and each delete")
			if mode == "delete-failure" || mode == "not-found" {
				assert.Equal(t, 2, deletes)
			} else {
				assert.Equal(t, 1, deletes)
			}
			require.True(t, apierrors.IsNotFound(f.downstream.Get(t.Context(), client.ObjectKeyFromObject(first), &corev1.ConfigMap{})))
			replacement := &corev1.ConfigMap{}
			require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKey{Namespace: inventoryNamespace, Name: inventoryName(before, controllerTestUUID(2))}, replacement))
			if mode == "not-found" {
				f.assertInventory(2, "desired")
			} else {
				require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(second), &corev1.ConfigMap{}))
			}
			if mode == "delete-failure" || mode == "read-failure" {
				assert.Equal(t, before, f.composition())
				f.restart()
				f.reconcileEvent()
				f.assertInventory(2, "desired")
				persisted := &corev1.ConfigMap{}
				require.NoError(t, f.downstream.Get(t.Context(), client.ObjectKeyFromObject(replacement), persisted))
				assert.Equal(t, replacement, persisted)
			}
		})
	}
}

func TestBackupControllerRecoveryStatusPatch(t *testing.T) {
	f := newControllerTestFixture(t, true, "desired")
	before := f.composition()
	f.controller.reader = nil
	writes := 0
	f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, cli client.Client, name string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			writes++
			assert.Equal(t, "status", name)
			assert.Equal(t, types.JSONPatchType, patch.Type())
			data, err := patch.Data(obj)
			require.NoError(t, err)
			expected := []statusPatch{
				{Op: "test", Path: "/metadata/uid", Value: before.UID},
				{Op: "test", Path: "/metadata/resourceVersion", Value: before.ResourceVersion},
				{Op: "test", Path: "/status/currentSynthesis/uuid", Value: before.Status.CurrentSynthesis.UUID},
				{Op: "add", Path: "/status/currentSynthesis/tombstoneRecoveryFinished", Value: apiv1.TombstoneRecoveryStatus{
					SynthesisUUID: before.Status.CurrentSynthesis.UUID, Status: true, Reason: reasonFinished,
				}},
				{Op: "add", Path: "/status/currentSynthesis/resourceSlices", Value: []*apiv1.ResourceSliceRef{{Name: "desired"}, {Name: "overflow"}}},
			}
			assert.JSONEq(t, inventoryTestJSON(t, expected), string(data))
			return cli.SubResource(name).Patch(ctx, obj, patch, opts...)
		},
	})
	refs := []*apiv1.ResourceSliceRef{{Name: "desired"}, {Name: "overflow"}, {Name: "overflow"}}
	require.NoError(t, f.controller.markTombstoneRecoveryFinished(t.Context(), before, reasonFinished, "", refs))
	require.Equal(t, 1, writes, "publish one terminal decision with no pending status or direct reads")
	got := f.composition()
	assert.Equal(t, []*apiv1.ResourceSliceRef{{Name: "desired"}, {Name: "overflow"}}, got.Status.CurrentSynthesis.ResourceSlices)
	assert.True(t, got.Status.CurrentSynthesis.TombstoneRecoveryRequired)
	require.NoError(t, f.controller.markTombstoneRecoveryFinished(t.Context(), got, reasonFinished, "", refs))
	assert.Equal(t, 1, writes, "an identical terminal status and references must not be rewritten")
	assert.Equal(t, got, f.composition())
}

func TestBackupControllerRecoveryErrorPublication(t *testing.T) {
	f := newControllerTestFixture(t, true, "desired")
	cause := apierrors.NewServiceUnavailable("downstream failure")
	patchFailure := apierrors.NewConflict(schema.GroupResource{Resource: "compositions"}, f.key.Name, fmt.Errorf("status conflict"))
	before := f.composition()
	f.controller.reader = nil
	writes := 0
	f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			writes++
			return patchFailure
		},
	})
	err := f.controller.recordTombstoneRecoveryError(t.Context(), before, reasonInventoryGetError, cause)
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, patchFailure)
	assert.Equal(t, 1, writes)
	err = f.controller.recordTombstoneRecoveryError(t.Context(), before, reasonSliceWriteError, fmt.Errorf("obsolete: %w", errSuperseded))
	require.ErrorIs(t, err, errSuperseded)
	assert.Equal(t, 1, writes, "superseded operations must not publish failure status")
	assert.Equal(t, before, f.composition())
}

func TestBackupControllerInventorySupersededBeforeCreate(t *testing.T) {
	for _, mode := range []string{"new-synthesis", "no-synthesis", "not-ready", "deleting", "not-found", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, false, "desired")
			f.history("removed")
			f.finish(reasonNotNeeded)
			f.ready()
			history := f.inventories()
			failure := apierrors.NewServiceUnavailable("fresh composition unavailable")
			f.controller.downstream = interceptor.NewClient(f.downstream, interceptor.Funcs{
				List: func(ctx context.Context, cli client.WithWatch, obj client.ObjectList, opts ...client.ListOption) error {
					switch mode {
					case "new-synthesis":
						f.updateStatus(func(syn *apiv1.Synthesis) { syn.UUID = controllerTestUUID(3) })
					case "no-synthesis":
						comp := f.composition()
						comp.Status.CurrentSynthesis = nil
						require.NoError(t, f.upstream.Status().Update(ctx, comp))
					case "not-ready":
						f.updateStatus(func(syn *apiv1.Synthesis) { syn.Ready = nil })
					case "not-found":
						comp := f.composition()
						comp.Finalizers = nil
						require.NoError(t, f.upstream.Update(ctx, comp))
						require.NoError(t, f.upstream.Delete(ctx, comp))
					case "deleting":
						require.NoError(t, f.upstream.Delete(ctx, f.composition()))
					}
					return cli.List(ctx, obj, opts...)
				},
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					t.Fatal("obsolete inventory must not be created")
					return nil
				},
			})
			if mode == "read-failure" {
				f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
					Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*apiv1.Composition); ok {
							return failure
						}
						return cli.Get(ctx, key, obj, opts...)
					},
				})
			}
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					t.Fatal("inventory failure must not publish status")
					return nil
				},
			})
			result, err := f.controller.Reconcile(t.Context(), ctrl.Request{NamespacedName: f.key})
			if mode == "read-failure" {
				require.ErrorIs(t, err, failure)
				assert.Equal(t, ctrl.Result{}, result)
			} else {
				require.NoError(t, err)
				assert.Equal(t, ctrl.Result{Requeue: true}, result)
			}
			assert.Equal(t, history, f.inventories())
		})
	}
}

func TestBackupControllerSliceReadFailures(t *testing.T) {
	for _, recording := range []bool{false, true} {
		for _, mode := range []string{"nil-ref", "empty-ref", "missing", "deleting", "invalid-manifest", "read-failure"} {
			t.Run(fmt.Sprintf("recording-%t/%s", recording, mode), func(t *testing.T) {
				f := newControllerTestFixture(t, !recording, "desired")
				f.history("removed")
				if recording {
					f.finish(reasonNotNeeded)
					f.ready()
				}
				switch mode {
				case "nil-ref":
					f.updateStatus(func(syn *apiv1.Synthesis) { syn.ResourceSlices = []*apiv1.ResourceSliceRef{nil} })
				case "empty-ref":
					f.updateStatus(func(syn *apiv1.Synthesis) { syn.ResourceSlices = []*apiv1.ResourceSliceRef{{}} })
				case "missing":
					f.updateStatus(func(syn *apiv1.Synthesis) { syn.ResourceSlices[0].Name = "missing" })
				case "deleting":
					slice := f.slice("desired")
					slice.Finalizers = []string{"test.example/hold"}
					require.NoError(t, f.upstream.Update(t.Context(), slice))
					require.NoError(t, f.upstream.Delete(t.Context(), slice))
				case "invalid-manifest":
					slice := f.slice("desired")
					slice.Spec.Resources[0].Manifest = "{"
					require.NoError(t, f.upstream.Update(t.Context(), slice))
				}
				failure := apierrors.NewServiceUnavailable("slice unavailable")
				f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
					Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						require.IsType(t, &apiv1.ResourceSlice{}, obj, "read failures must not cause extra Composition reads")
						if mode == "read-failure" {
							return failure
						}
						return cli.Get(ctx, key, obj, opts...)
					},
				})
				before, history, slice := f.composition(), f.inventories(), f.slice("desired")
				err := f.reconcile()
				require.Error(t, err)
				switch mode {
				case "read-failure":
					assert.ErrorIs(t, err, failure)
				case "nil-ref", "empty-ref":
					assert.ErrorContains(t, err, "reference 0 has no name")
				case "missing":
					assert.True(t, apierrors.IsNotFound(err))
				case "deleting":
					assert.ErrorContains(t, err, "is being deleted")
				case "invalid-manifest":
					assert.ErrorContains(t, err, "invalid manifest JSON")
				}
				if recording {
					assert.Equal(t, before, f.composition())
				} else {
					status := f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished
					require.NotNil(t, status)
					assert.False(t, status.Status)
					assert.Equal(t, reasonSliceReadError, status.Reason)
					assert.Equal(t, before.Status.CurrentSynthesis.UUID, status.SynthesisUUID)
					assert.Equal(t, err.Error(), status.Message)
				}
				assert.Equal(t, history, f.inventories())
				assert.Equal(t, slice, f.slice("desired"))
			})
		}
	}
}
