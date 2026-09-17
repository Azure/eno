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
	"k8s.io/apimachinery/pkg/labels"
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
		client: f.upstream, reader: f.upstream, downstream: f.downstream, enabled: true,
		operations: map[types.NamespacedName]*operation{},
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
		client: old.client, reader: old.reader, downstream: old.downstream, enabled: old.enabled,
		resourceFilter: old.resourceFilter, compositionNamespace: old.compositionNamespace,
		compositionSelector: old.compositionSelector, operations: map[types.NamespacedName]*operation{},
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
			assert.False(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryFinished.Status)
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
			assert.False(t, f.composition().Status.CurrentSynthesis.TombstoneRecoveryRequired)
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

func TestBackupControllerOutageRecovery(t *testing.T) {
	f := newControllerTestFixture(t, true, "desired")
	old := f.history("desired", "removed")
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
	f.ready()
	require.NoError(t, f.reconcile())
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

func TestBackupControllerRetainsUnresolvedInventory(t *testing.T) {
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
			assert.Equal(t, []corev1.ConfigMap{*old}, f.inventories(),
				"an invalid or incomplete historical inventory must not be replaced or deleted")
		})
	}
}

func TestBackupControllerOwnershipAndDisabled(t *testing.T) {
	for _, mode := range []string{"disabled", "namespace", "selector", "CEL"} {
		t.Run(mode, func(t *testing.T) {
			f := newControllerTestFixture(t, true, "desired")
			switch mode {
			case "disabled":
				f.controller.enabled = false
				f.controller.client = nil
				f.controller.reader = nil
				require.NoError(t, NewController(nil, Options{}), "disabled backup must not access the manager or register controllers")
			case "namespace":
				f.controller.compositionNamespace = "other"
			case "selector":
				f.controller.compositionSelector = labels.SelectorFromSet(labels.Set{"owner": "other"})
			case "CEL":
				filter, err := enocel.Parse(`composition.metadata.labels.owner == "other"`)
				require.NoError(t, err)
				f.controller.resourceFilter = filter
			}
			before := f.composition()
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
					t.Error("filtered Composition must not receive status writes")
					return fmt.Errorf("unexpected status patch")
				},
			})
			if mode == "disabled" {
				f.controller.client = nil
			}
			// Any downstream operation is a test failure, including discovery.
			f.controller.downstream = nil
			require.NoError(t, f.reconcile())
			after := f.composition()
			assert.Equal(t, before, after)
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
		{name: "legacy-addon", overlayType: "addon", matches: true},
		{name: "legacy-ccp", overlayType: "ccp"},
		{name: "legacy-unlabeled"},
		{name: "addon-excludes-overlay-managed", componentType: "addon", overlayType: "addon"},
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
				assert.Empty(t, f.controller.operations)
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
		{name: "inventory-addon", inventoryType: "addon", matches: true},
		{name: "inventory-ccp", inventoryType: "ccp"},
		{name: "empty-addon", componentType: "addon", matches: true},
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
				f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
					SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
						t.Error("filtered Composition must not receive status writes")
						return fmt.Errorf("unexpected status patch")
					},
				})
				require.NoError(t, f.reconcile())
				assert.Equal(t, before, f.composition())
				assert.Empty(t, f.controller.operations)
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
			assert.Empty(t, f.controller.operations)
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
	require.NoError(t, f.reconcile())
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
				require.NoError(t, f.reconcile())
				if expected != nil {
					break
				}
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
