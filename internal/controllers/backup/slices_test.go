package backup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func sliceTestManifest(t *testing.T, name string, size int, deleted bool) apiv1.Manifest {
	t.Helper()
	obj := map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]string{"name": name, "namespace": "workloads"},
		"data":     map[string]string{"padding": ""},
	}
	base := inventoryTestJSON(t, obj)
	require.GreaterOrEqual(t, size, len(base))
	obj["data"] = map[string]string{"padding": strings.Repeat("x", size-len(base))}
	manifest := inventoryTestJSON(t, obj)
	require.Len(t, manifest, size)
	require.True(t, json.Valid([]byte(manifest)))
	return apiv1.Manifest{Manifest: manifest, Deleted: deleted}
}

func sliceTestExisting(f *controllerTestFixture, resources [][]apiv1.Manifest) []apiv1.ResourceSlice {
	f.t.Helper()
	template := f.slice("desired")
	var slices []apiv1.ResourceSlice
	var refs []*apiv1.ResourceSliceRef
	if len(resources) == 0 {
		require.NoError(f.t, f.upstream.Delete(f.t.Context(), template))
	}
	for i, manifests := range resources {
		slice := template.DeepCopy()
		slice.Spec.Resources = manifests
		if i == 0 {
			require.NoError(f.t, f.upstream.Update(f.t.Context(), slice))
		} else {
			slice.Name = fmt.Sprintf("desired-%d", i)
			slice.UID, slice.ResourceVersion = "", ""
			require.NoError(f.t, f.upstream.Create(f.t.Context(), slice))
		}
		for j := range manifests {
			message := fmt.Sprintf("resource-%d-%d", i, j)
			slice.Status.Resources = append(slice.Status.Resources, apiv1.ResourceState{
				Reconciled: j%2 == 0, Deleted: manifests[j].Deleted,
				Ready: f.composition().Status.CurrentSynthesis.Synthesized.DeepCopy(), ReconciliationError: &message,
			})
		}
		require.NoError(f.t, f.upstream.Status().Update(f.t.Context(), slice))
		slices = append(slices, *f.slice(slice.Name))
		refs = append(refs, &apiv1.ResourceSliceRef{Name: slice.Name})
	}
	f.updateStatus(func(syn *apiv1.Synthesis) {
		syn.ResourceSlices = refs
	})
	return slices
}

func sliceTestContents(t *testing.T, cli client.Client, wantSlices int, want []apiv1.Manifest) {
	t.Helper()
	list := &apiv1.ResourceSliceList{}
	require.NoError(t, cli.List(t.Context(), list))
	require.Len(t, list.Items, wantSlices)
	var got []apiv1.Manifest
	for _, slice := range list.Items {
		size := 0
		for _, manifest := range slice.Spec.Resources {
			require.True(t, json.Valid([]byte(manifest.Manifest)))
			size += len(manifest.Manifest)
			got = append(got, manifest)
		}
		assert.LessOrEqual(t, size, resource.MaxSliceJSONBytes, "slice %s", slice.Name)
	}
	assert.ElementsMatch(t, want, got, "every original manifest and tombstone must occur exactly as often as expected")
}

func TestWriteTombstonesPacking(t *testing.T) {
	const limit = resource.MaxSliceJSONBytes
	for _, tc := range []struct {
		name       string
		existing   [][]int
		tombstones []int
		additions  [][]int
		overflow   [][]int
	}{
		{name: "no slices or tombstones"},
		{name: "no tombstones", existing: [][]int{{256, 300}}, additions: [][]int{nil}},
		{name: "append preserves manifest and status indexes", existing: [][]int{{256, 300}}, tombstones: []int{512, 512}, additions: [][]int{{0, 1}}},
		{name: "first fit across partially full slices", existing: [][]int{{limit - 768}, {limit - 1024}, {limit}}, tombstones: []int{512, 1024, 256}, additions: [][]int{{0, 2}, {1}, nil}},
		{name: "exact fit", existing: [][]int{{limit - 512}}, tombstones: []int{512}, additions: [][]int{{0}}},
		{name: "overflow without existing slices", tombstones: []int{512, 512}, overflow: [][]int{{0, 1}}},
		{name: "one byte over remaining capacity", existing: [][]int{{limit - 511}}, tombstones: []int{512}, additions: [][]int{nil}, overflow: [][]int{{0}}},
		{name: "append and overflow", existing: [][]int{{limit - 512}}, tombstones: []int{512, 512, 512}, additions: [][]int{{0}}, overflow: [][]int{{1, 2}}},
		{name: "multiple overflow slices", tombstones: []int{limit / 2, limit / 2, limit / 2, limit / 2, 512}, overflow: [][]int{{0, 1}, {2, 3}, {4}}},
		{name: "exact limit overflow manifest", tombstones: []int{limit}, overflow: [][]int{{0}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerTestFixture(t, true)
			var existing [][]apiv1.Manifest
			var all []apiv1.Manifest
			for i, sizes := range tc.existing {
				var manifests []apiv1.Manifest
				for j, size := range sizes {
					manifest := sliceTestManifest(t, fmt.Sprintf("existing-%d-%d", i, j), size, j%2 == 1)
					manifests = append(manifests, manifest)
					all = append(all, manifest)
				}
				existing = append(existing, manifests)
			}
			slices := sliceTestExisting(f, existing)
			before := (&apiv1.ResourceSliceList{Items: slices}).DeepCopy()
			comp := f.composition()
			var tombstones []apiv1.Manifest
			for i, size := range tc.tombstones {
				tombstones = append(tombstones, sliceTestManifest(t, fmt.Sprintf("removed-%d", i), size, true))
			}
			all = append(all, tombstones...)
			var events []string
			f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					require.IsType(t, &apiv1.Composition{}, obj)
					events = append(events, "current")
					return cli.Get(ctx, key, obj, opts...)
				},
			})
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					events = append(events, "patch:"+obj.GetName())
					data, err := patch.Data(obj)
					require.NoError(t, err)
					var body struct {
						Metadata metav1.ObjectMeta `json:"metadata"`
						Status   json.RawMessage   `json:"status"`
					}
					require.NoError(t, json.Unmarshal(data, &body))
					require.Equal(t, f.slice(obj.GetName()).ResourceVersion, body.Metadata.ResourceVersion, "patch must use optimistic locking")
					require.Empty(t, body.Status)
					return cli.Patch(ctx, obj, patch, opts...)
				},
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					events = append(events, "create:"+obj.GetName())
					return cli.Create(ctx, obj, opts...)
				},
			})

			refs, err := f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
			require.NoError(t, err)
			assert.Equal(t, before.Items, slices, "caller snapshots must not be mutated")
			assert.Equal(t, comp, f.composition(), "slice writes must not publish references or recovery status")
			var wantEvents []string
			for i, original := range before.Items {
				want := original.DeepCopy()
				for _, index := range tc.additions[i] {
					want.Spec.Resources = append(want.Spec.Resources, tombstones[index])
				}
				got := f.slice(original.Name)
				assert.Equal(t, want.Spec, got.Spec)
				assert.Equal(t, original.Status, got.Status)
				if len(tc.additions[i]) == 0 {
					assert.Equal(t, &original, got, "untouched slices must not be written")
				} else {
					wantEvents = append(wantEvents, "current", "patch:"+original.Name)
				}
			}
			require.Len(t, refs, len(tc.overflow), "only newly needed overflow slices should be referenced")
			for i, indexes := range tc.overflow {
				var manifests []apiv1.Manifest
				for _, index := range indexes {
					manifests = append(manifests, tombstones[index])
				}
				want, err := recoverySlice(comp, manifests)
				require.NoError(t, err)
				assert.Equal(t, want.Name, refs[i].Name)
				got := f.slice(refs[i].Name)
				assert.Equal(t, want.Spec, got.Spec)
				assert.Empty(t, got.Status)
				wantEvents = append(wantEvents, "current", "create:"+want.Name)
				if tc.name == "exact limit overflow manifest" {
					data, err := json.Marshal(got)
					require.NoError(t, err)
					assert.Greater(t, len(data), limit, "the byte limit applies to manifests, not the serialized slice")
				}
			}
			assert.Equal(t, wantEvents, events, "check current synthesis before each write and patch each changed slice only once")
			sliceTestContents(t, f.upstream, len(slices)+len(refs), all)
		})
	}
}

func TestWriteTombstonesOversized(t *testing.T) {
	for _, withExisting := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing-%t", withExisting), func(t *testing.T) {
			f := newControllerTestFixture(t, true)
			var existing [][]apiv1.Manifest
			if withExisting {
				existing = [][]apiv1.Manifest{{sliceTestManifest(t, "existing", 256, false)}}
			}
			slices := sliceTestExisting(f, existing)
			before := &apiv1.ResourceSliceList{}
			require.NoError(t, f.upstream.List(t.Context(), before))
			writes := 0
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					writes++
					return cli.Patch(ctx, obj, patch, opts...)
				},
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					writes++
					return cli.Create(ctx, obj, opts...)
				},
			})
			refs, err := f.controller.writeTombstones(t.Context(), f.composition(), slices, []apiv1.Manifest{
				sliceTestManifest(t, "valid-before-oversized", 512, true),
				sliceTestManifest(t, "oversized", resource.MaxSliceJSONBytes+1, true),
			})
			require.EqualError(t, err, fmt.Sprintf("recovery tombstone exceeds the ResourceSlice manifest byte limit: %d > %d", resource.MaxSliceJSONBytes+1, resource.MaxSliceJSONBytes))
			assert.Nil(t, refs)
			assert.Zero(t, writes, "reject the whole batch before persisting even a valid prefix")
			after := &apiv1.ResourceSliceList{}
			require.NoError(t, f.upstream.List(t.Context(), after))
			assert.Equal(t, before, after)
		})
	}
}

func TestWriteTombstonesPartialFailureRetry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		capacity      []int
		failPatch     int
		failCreate    int
		large         bool
		wantMissing   int
		wantOverflow  int
		wantCollision int
	}{
		{name: "second append fails", capacity: []int{1, 2}, failPatch: 2, wantMissing: 2},
		{name: "create fails after append", capacity: []int{1}, failCreate: 1, wantMissing: 2, wantOverflow: 1},
		{name: "second overflow create fails", failCreate: 2, large: true, wantMissing: 3, wantOverflow: 3, wantCollision: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerTestFixture(t, true)
			var history []inventoryResource
			for _, name := range []string{"removed-a", "removed-b", "removed-c"} {
				res := controllerTestResource(name)
				if tc.large {
					res.Labels = map[string]string{"padding": strings.Repeat("x", resource.MaxSliceJSONBytes/2)}
				}
				history = append(history, res)
			}
			tombstones, err := missingTombstones(nil, history)
			require.NoError(t, err)
			require.Len(t, tombstones, 3)
			var existing [][]apiv1.Manifest
			var all []apiv1.Manifest
			for i, count := range tc.capacity {
				manifest := sliceTestManifest(t, fmt.Sprintf("existing-%d", i), resource.MaxSliceJSONBytes-count*len(tombstones[0].Manifest), false)
				existing = append(existing, []apiv1.Manifest{manifest})
				all = append(all, manifest)
			}
			slices := sliceTestExisting(f, existing)
			comp := f.composition()
			patches, creates, collisions := 0, 0, 0
			rejection := apierrors.NewServiceUnavailable("injected slice write failure")
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patches++
					if patches == tc.failPatch {
						return rejection
					}
					return cli.Patch(ctx, obj, patch, opts...)
				},
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					creates++
					if creates == tc.failCreate {
						return rejection
					}
					err := cli.Create(ctx, obj, opts...)
					if apierrors.IsAlreadyExists(err) {
						collisions++
					}
					return err
				},
			})
			refs, err := f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
			require.ErrorIs(t, err, rejection)
			if tc.failPatch != 0 {
				assert.ErrorContains(t, err, `appending tombstones to ResourceSlice "desired-1"`)
			} else {
				assert.ErrorContains(t, err, "creating recovery ResourceSlice")
			}
			assert.Nil(t, refs)
			assert.Equal(t, comp, f.composition())
			persistedCount := len(slices)
			if tc.large {
				persistedCount++
			}
			persisted := append(append([]apiv1.Manifest(nil), all...), tombstones[0])
			sliceTestContents(t, f.upstream, persistedCount, persisted)

			// Reload only published references, just as the next controller invocation does.
			reloaded, err := f.controller.loadCurrentSynthesisResourceSlices(t.Context(), f.composition())
			require.NoError(t, err)
			missing, err := missingTombstones(reloaded, history)
			require.NoError(t, err)
			require.Equal(t, tombstones[len(tombstones)-tc.wantMissing:], missing)
			refs, err = f.controller.writeTombstones(t.Context(), f.composition(), reloaded, missing)
			require.NoError(t, err)
			require.Len(t, refs, tc.wantOverflow)
			assert.Equal(t, tc.wantCollision, collisions)
			for _, original := range slices {
				got := f.slice(original.Name)
				assert.Equal(t, original.Spec.Resources, got.Spec.Resources[:len(original.Spec.Resources)])
				assert.Equal(t, original.Status, got.Status)
			}
			all = append(all, tombstones...)
			sliceTestContents(t, f.upstream, len(slices)+tc.wantOverflow, all)
			for _, ref := range refs {
				got := f.slice(ref.Name)
				want, err := recoverySlice(comp, got.Spec.Resources)
				require.NoError(t, err)
				assert.Equal(t, want.Name, ref.Name)
				assert.Equal(t, want.Spec, got.Spec)
			}

			f.updateStatus(func(syn *apiv1.Synthesis) {
				syn.ResourceSlices = append(syn.ResourceSlices, refs...)
			})
			reloaded, err = f.controller.loadCurrentSynthesisResourceSlices(t.Context(), f.composition())
			require.NoError(t, err)
			missing, err = missingTombstones(reloaded, history)
			require.NoError(t, err)
			require.Empty(t, missing)
			beforePatches, beforeCreates := patches, creates
			refs, err = f.controller.writeTombstones(t.Context(), f.composition(), reloaded, missing)
			require.NoError(t, err)
			assert.Empty(t, refs)
			assert.Equal(t, beforePatches, patches)
			assert.Equal(t, beforeCreates, creates)
			sliceTestContents(t, f.upstream, len(slices)+tc.wantOverflow, all)
		})
	}
}

func TestWriteTombstonesUnpublishedOverflowRetry(t *testing.T) {
	f := newControllerTestFixture(t, true)
	slices := sliceTestExisting(f, nil)
	comp := f.composition()
	history := []inventoryResource{controllerTestResource("removed")}
	tombstones, err := missingTombstones(slices, history)
	require.NoError(t, err)
	creates, collisions := 0, 0
	f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
		Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			creates++
			err := cli.Create(ctx, obj, opts...)
			if apierrors.IsAlreadyExists(err) {
				collisions++
			}
			return err
		},
	})
	first, err := f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
	require.NoError(t, err)
	require.Len(t, first, 1)
	persisted := f.slice(first[0].Name)
	assert.Equal(t, comp, f.composition(), "simulate a crash before status/reference publication")
	reloaded, err := f.controller.loadCurrentSynthesisResourceSlices(t.Context(), f.composition())
	require.NoError(t, err)
	missing, err := missingTombstones(reloaded, history)
	require.NoError(t, err)
	require.Equal(t, tombstones, missing, "unpublished overflow is not part of the current slice references")
	retry, err := f.controller.writeTombstones(t.Context(), f.composition(), reloaded, missing)
	require.NoError(t, err)
	assert.Equal(t, first, retry)
	assert.Equal(t, 2, creates)
	assert.Equal(t, 1, collisions, "retry must exercise the deterministic AlreadyExists path")
	assert.Equal(t, persisted, f.slice(first[0].Name), "matching overflow must not be rewritten")
	assert.Equal(t, comp, f.composition())
	sliceTestContents(t, f.upstream, 1, tombstones)
}

func TestWriteTombstonesOverflowCollision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*apiv1.ResourceSlice)
		delete bool
	}{
		{name: "missing controller owner", mutate: func(slice *apiv1.ResourceSlice) {
			slice.OwnerReferences = nil
		}},
		{name: "non controller owner", mutate: func(slice *apiv1.ResourceSlice) {
			controller := false
			slice.OwnerReferences[0].Controller = &controller
		}},
		{name: "different owner UID", mutate: func(slice *apiv1.ResourceSlice) {
			slice.OwnerReferences[0].UID = "another-composition"
		}},
		{name: "different owner kind", mutate: func(slice *apiv1.ResourceSlice) {
			slice.OwnerReferences[0].Kind = "Symphony"
		}},
		{name: "different synthesis", mutate: func(slice *apiv1.ResourceSlice) {
			slice.Spec.SynthesisUUID = controllerTestUUID(3)
		}},
		{name: "different manifest", mutate: func(slice *apiv1.ResourceSlice) {
			slice.Spec.Resources[0].Manifest = strings.ReplaceAll(slice.Spec.Resources[0].Manifest, "removed", "another")
		}},
		{name: "different tombstone flag", mutate: func(slice *apiv1.ResourceSlice) {
			slice.Spec.Resources[0].Deleted = false
		}},
		{name: "deleting matching slice", delete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerTestFixture(t, true)
			slices := sliceTestExisting(f, nil)
			comp := f.composition()
			tombstones := []apiv1.Manifest{sliceTestManifest(t, "removed", 256, true)}
			collision, err := recoverySlice(comp, tombstones)
			require.NoError(t, err)
			collision = collision.DeepCopy()
			if tc.mutate != nil {
				tc.mutate(collision)
			}
			require.NoError(t, f.upstream.Create(t.Context(), collision))
			if tc.delete {
				require.NoError(t, f.upstream.Delete(t.Context(), collision))
			}
			before := f.slice(collision.Name)
			if tc.delete {
				require.NotNil(t, before.DeletionTimestamp)
			}
			refs, err := f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
			require.EqualError(t, err, fmt.Sprintf("existing recovery ResourceSlice %q does not match this operation or is being deleted", collision.Name))
			assert.Nil(t, refs)
			assert.Equal(t, before, f.slice(collision.Name))
			assert.Equal(t, comp, f.composition())
			sliceTestContents(t, f.upstream, 1, before.Spec.Resources)
		})
	}
}

func TestWriteTombstonesExistingOverflowReadError(t *testing.T) {
	f := newControllerTestFixture(t, true)
	slices := sliceTestExisting(f, nil)
	comp := f.composition()
	tombstones := []apiv1.Manifest{sliceTestManifest(t, "removed", 256, true)}
	existing, err := recoverySlice(comp, tombstones)
	require.NoError(t, err)
	require.NoError(t, f.upstream.Create(t.Context(), existing))
	rejection := apierrors.NewServiceUnavailable("injected recovery slice read failure")
	f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
		Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*apiv1.ResourceSlice); ok {
				require.Equal(t, client.ObjectKeyFromObject(existing), key)
				return rejection
			}
			return cli.Get(ctx, key, obj, opts...)
		},
	})
	refs, err := f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
	require.ErrorIs(t, err, rejection)
	assert.ErrorContains(t, err, fmt.Sprintf("reading existing recovery ResourceSlice %q", existing.Name))
	assert.Nil(t, refs)
	assert.Equal(t, existing, f.slice(existing.Name))
	sliceTestContents(t, f.upstream, 1, tombstones)
}

func TestWriteTombstonesChecksCurrentBeforeEachWrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing int
		stopAt   int
		readFail bool
	}{
		{name: "before first append", existing: 2, stopAt: 1},
		{name: "between appends", existing: 2, stopAt: 2},
		{name: "between append and create", existing: 1, stopAt: 2},
		{name: "before first create", stopAt: 1},
		{name: "between creates", stopAt: 2},
		{name: "current read error", existing: 1, stopAt: 1, readFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerTestFixture(t, true)
			const size = resource.MaxSliceJSONBytes/2 + 1
			var existing [][]apiv1.Manifest
			var all []apiv1.Manifest
			for i := 0; i < tc.existing; i++ {
				manifest := sliceTestManifest(t, fmt.Sprintf("existing-%d", i), resource.MaxSliceJSONBytes-size, false)
				existing = append(existing, []apiv1.Manifest{manifest})
				all = append(all, manifest)
			}
			slices := sliceTestExisting(f, existing)
			comp := f.composition()
			tombstones := []apiv1.Manifest{
				sliceTestManifest(t, "removed-a", size, true),
				sliceTestManifest(t, "removed-b", size, true),
			}
			checks, writes := 0, 0
			rejection := apierrors.NewServiceUnavailable("injected current composition read failure")
			f.controller.reader = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Get: func(ctx context.Context, cli client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					require.IsType(t, &apiv1.Composition{}, obj)
					checks++
					if checks == tc.stopAt {
						if tc.readFail {
							return rejection
						}
						f.updateStatus(func(syn *apiv1.Synthesis) {
							syn.UUID = controllerTestUUID(3)
						})
					}
					return cli.Get(ctx, key, obj, opts...)
				},
			})
			f.controller.client = interceptor.NewClient(f.upstream, interceptor.Funcs{
				Patch: func(ctx context.Context, cli client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					writes++
					return cli.Patch(ctx, obj, patch, opts...)
				},
				Create: func(ctx context.Context, cli client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					writes++
					return cli.Create(ctx, obj, opts...)
				},
			})
			refs, err := f.controller.writeTombstones(t.Context(), comp, slices, tombstones)
			if tc.readFail {
				require.ErrorIs(t, err, rejection)
			} else {
				require.ErrorIs(t, err, errSuperseded)
			}
			assert.Nil(t, refs)
			assert.Equal(t, tc.stopAt, checks)
			assert.Equal(t, tc.stopAt-1, writes)
			all = append(all, tombstones[:tc.stopAt-1]...)
			sliceTestContents(t, f.upstream, max(tc.existing, tc.stopAt-1), all)
			for _, original := range slices {
				got := f.slice(original.Name)
				assert.Equal(t, original.Spec.Resources, got.Spec.Resources[:len(original.Spec.Resources)])
				assert.Equal(t, original.Status, got.Status)
			}
			assert.Equal(t, comp.Status.CurrentSynthesis.ResourceSlices, f.composition().Status.CurrentSynthesis.ResourceSlices)
		})
	}
}

func TestRecoverySlice(t *testing.T) {
	comp := inventoryTestComposition()
	manifests := []apiv1.Manifest{
		sliceTestManifest(t, "removed-a", 256, true),
		sliceTestManifest(t, "removed-b", 256, true),
	}
	slice, err := recoverySlice(comp, manifests)
	require.NoError(t, err)
	data, err := json.Marshal(manifests)
	require.NoError(t, err)
	operation := sha256.Sum256([]byte(string(comp.UID) + "/" + comp.Status.CurrentSynthesis.UUID))
	contents := sha256.Sum256(data)
	assert.Equal(t, fmt.Sprintf("%s-%x-%x", comp.Name, operation[:8], contents[:16]), slice.Name)
	assert.Empty(t, slice.GenerateName)
	assert.Equal(t, comp.Namespace, slice.Namespace)
	assert.Equal(t, map[string]string{manager.SynthesisIDLabelKey: comp.Status.CurrentSynthesis.UUID}, slice.Labels)
	assert.Equal(t, []metav1.OwnerReference{*metav1.NewControllerRef(comp, apiv1.SchemeGroupVersion.WithKind("Composition"))}, slice.OwnerReferences)
	assert.Equal(t, []string{"eno.azure.io/cleanup"}, slice.Finalizers)
	assert.Equal(t, apiv1.ResourceSliceSpec{SynthesisUUID: comp.Status.CurrentSynthesis.UUID, Resources: manifests}, slice.Spec)
	assert.Empty(t, slice.Status)
	retry, err := recoverySlice(comp.DeepCopy(), append([]apiv1.Manifest(nil), manifests...))
	require.NoError(t, err)
	assert.Equal(t, slice, retry)

	for _, tc := range []struct {
		name   string
		mutate func(*apiv1.Composition, []apiv1.Manifest)
	}{
		{name: "composition UID", mutate: func(comp *apiv1.Composition, manifests []apiv1.Manifest) {
			comp.UID = "recreated-composition"
		}},
		{name: "synthesis UUID", mutate: func(comp *apiv1.Composition, manifests []apiv1.Manifest) {
			comp.Status.CurrentSynthesis.UUID = controllerTestUUID(3)
		}},
		{name: "manifest content", mutate: func(comp *apiv1.Composition, manifests []apiv1.Manifest) {
			manifests[0].Manifest = strings.ReplaceAll(manifests[0].Manifest, "removed-a", "removed-c")
		}},
		{name: "manifest order", mutate: func(comp *apiv1.Composition, manifests []apiv1.Manifest) {
			manifests[0], manifests[1] = manifests[1], manifests[0]
		}},
		{name: "tombstone flag", mutate: func(comp *apiv1.Composition, manifests []apiv1.Manifest) {
			manifests[0].Deleted = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := comp.DeepCopy()
			manifests := append([]apiv1.Manifest(nil), manifests...)
			tc.mutate(comp, manifests)
			got, err := recoverySlice(comp, manifests)
			require.NoError(t, err)
			assert.NotEqual(t, slice.Name, got.Name)
			assert.True(t, strings.HasPrefix(got.Name, comp.Name+"-"))
		})
	}
}

func TestRecoverySliceName(t *testing.T) {
	for _, name := range []string{
		"example-composition",
		strings.Repeat("a", 253),
		strings.Repeat("a", 202) + "." + strings.Repeat("b", 50),
		strings.Repeat("a", 202) + "-" + strings.Repeat("b", 50),
	} {
		t.Run(name, func(t *testing.T) {
			comp := inventoryTestComposition()
			comp.Name = name
			manifests := []apiv1.Manifest{inventoryTestManifest(t, inventoryTestResource("removed"))}
			slice, err := recoverySlice(comp, manifests)
			require.NoError(t, err)
			assert.Empty(t, validation.IsDNS1123Subdomain(slice.Name))
			assert.LessOrEqual(t, len(slice.Name), validation.DNS1123SubdomainMaxLength)
			prefix := strings.TrimRight(name[:min(len(name), 203)], ".-")
			assert.True(t, strings.HasPrefix(slice.Name, prefix+"-"))
			retry, err := recoverySlice(comp, manifests)
			require.NoError(t, err)
			assert.Equal(t, slice.Name, retry.Name)
			comp.Status.CurrentSynthesis.UUID = "another-synthesis"
			next, err := recoverySlice(comp, manifests)
			require.NoError(t, err)
			assert.NotEqual(t, slice.Name, next.Name)
		})
	}
}
