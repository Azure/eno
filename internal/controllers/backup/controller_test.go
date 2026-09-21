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

// NotNeeded must not read downstream history.

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
			assert.True(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryRequired)
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

func TestBackupControllerRecoverySliceReadFailures(t *testing.T) {
	for _, mode := range []string{"nil-ref", "empty-ref", "missing", "deleting", "invalid-manifest", "read-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			f.history("removed")
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
			status := f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished
			require.NotNil(t, status)
			assert.False(t, status.Status)
			assert.Equal(t, reasonSliceReadError, status.Reason)
			assert.Equal(t, before.Status.CurrentSynthesis.UUID, status.SynthesisUUID)
			assert.Equal(t, err.Error(), status.Message)
			assert.Equal(t, history, f.inventories())
			assert.Equal(t, slice, f.slice("desired"))
		})
	}
}
