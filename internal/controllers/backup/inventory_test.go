package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const inventoryTestPatch = `{"apiVersion":"eno.azure.io/v1","kind":"Patch","metadata":{"name":"patched","namespace":"workloads"},"patch":{"apiVersion":"apps/v1beta1","kind":"Deployment","ops":[]}}`

func inventoryTestComposition() *apiv1.Composition {
	synthesized := metav1.NewTime(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "composition", Namespace: "control", UID: "composition-uid",
			Labels:      map[string]string{"owner": "team"},
			Annotations: map[string]string{"example.com/source": "saved"},
		},
		Spec: apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: "synthesizer"}},
		Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{
			UUID: "00000000-0000-4000-8000-000000000001", Synthesized: &synthesized,
		}},
	}
}

func inventoryTestResource(name string) inventoryResource {
	return inventoryResource{
		Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "workloads", Name: name,
		Labels: map[string]string{"owner": "team", "app": name},
		Annotations: map[string]string{
			"eno.azure.io/readiness-group": "3",
			"eno.azure.io/deletion-group":  "4",
		},
	}
}

func inventoryTestJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	return string(payload)
}

func inventoryTestManifest(t *testing.T, res inventoryResource) apiv1.Manifest {
	t.Helper()
	return apiv1.Manifest{Manifest: inventoryTestJSON(t, map[string]any{
		"apiVersion": schema.GroupVersion{Group: res.Group, Version: res.Version}.String(),
		"kind":       res.Kind,
		"metadata": map[string]any{
			"name": res.Name, "namespace": res.Namespace,
			"labels": res.Labels, "annotations": res.Annotations,
		},
	})}
}

func TestBackupInventoryRoundTrip(t *testing.T) {
	comp := inventoryTestComposition()
	res := inventoryTestResource("desired")
	snapshot, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
	}})
	require.NoError(t, err)
	assert.JSONEq(t, inventoryTestJSON(t, []inventoryResource{res}), snapshot.Data[inventoryDataKey])
	assert.Equal(t, map[string]string{
		inventoryFormatVersionAnnotation:        "1",
		inventoryCompositionNamespaceAnnotation: comp.Namespace,
		inventorySynthesizerNameAnnotation:      comp.Spec.Synthesizer.Name,
		inventorySynthesisUUIDAnnotation:        comp.Status.CurrentSynthesis.UUID,
		inventorySynthesizedAnnotation:          "2026-09-16T12:00:00Z",
	}, snapshot.Annotations)
	assert.Equal(t, "kube-system", snapshot.Namespace)

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	require.NoError(t, cli.Create(t.Context(), snapshot))
	stored := &corev1.ConfigMap{}
	require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(snapshot), stored))
	assert.Equal(t, snapshot.Data, stored.Data)

	recreated := comp.DeepCopy()
	recreated.Name, recreated.UID = "replacement", "replacement-uid"
	recreated.Labels, recreated.Annotations = nil, nil
	recreated.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
	recovered, err := decodeInventorySnapshot(recreated, *stored)
	require.NoError(t, err)
	assert.Equal(t, []inventoryResource{res}, recovered)
	matches, err := inventoriesMatch(comp, stored, snapshot)
	require.NoError(t, err)
	assert.True(t, matches)
	stored.Annotations["example.com/note"] = "unrelated"
	matches, err = inventoriesMatch(comp, stored, snapshot)
	require.NoError(t, err)
	assert.True(t, matches)

	t.Run("empty inventory", func(t *testing.T) {
		empty, err := makeInventory(comp, nil)
		require.NoError(t, err)
		decoded, err := decodeInventorySnapshot(comp, *empty)
		require.NoError(t, err)
		assert.NotNil(t, decoded)
		assert.Empty(t, decoded)
		assert.Equal(t, "[]", empty.Data[inventoryDataKey])
	})

	t.Run("wire normalization", func(t *testing.T) {
		comp := inventoryTestComposition()
		comp.Status.CurrentSynthesis.Synthesized.Time = comp.Status.CurrentSynthesis.Synthesized.Add(time.Millisecond).In(time.FixedZone("offset", 3600))
		res := inventoryTestResource("desired")
		res.Labels = map[string]string{}
		built, err := makeInventory(comp, []apiv1.ResourceSlice{{
			Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
		}})
		require.NoError(t, err)
		item := built.DeepCopy()
		item.Annotations[inventorySynthesizedAnnotation] = comp.Status.CurrentSynthesis.Synthesized.Format(time.RFC3339)
		original := item.DeepCopy()
		decoded, err := decodeInventorySnapshot(comp, *item)
		require.NoError(t, err)
		assert.Len(t, decoded, 1)
		matches, err := inventoriesMatch(comp, item, built)
		require.NoError(t, err)
		assert.True(t, matches)
		assert.Equal(t, original, item)
	})

	t.Run("resource comparison uses stored order", func(t *testing.T) {
		resources := []inventoryResource{inventoryTestResource("first"), inventoryTestResource("second")}
		intended := snapshot.DeepCopy()
		intended.Data[inventoryDataKey] = inventoryTestJSON(t, resources)
		existing := intended.DeepCopy()
		matches, err := inventoriesMatch(comp, existing, intended)
		require.NoError(t, err)
		assert.True(t, matches)
		slices.Reverse(resources)
		existing.Data[inventoryDataKey] = inventoryTestJSON(t, resources)
		matches, err = inventoriesMatch(comp, existing, intended)
		require.NoError(t, err)
		assert.False(t, matches)
	})

	t.Run("decoding preserves resource entries", func(t *testing.T) {
		item := snapshot.DeepCopy()
		resources := []inventoryResource{inventoryTestResource("second"), inventoryTestResource("first")}
		resources[0].Labels = map[string]string{}
		item.Data[inventoryDataKey] = inventoryTestJSON(t, resources)
		item.Data[inventoryDataKey] = strings.Replace(item.Data[inventoryDataKey], `"name":"second"`, `"name":"second","labels":{}`, 1)
		item.Annotations[inventoryFormatVersionAnnotation] = "other"
		item.Annotations[inventorySynthesizedAnnotation] = "unused by decoding"
		original := item.DeepCopy()
		decoded, err := decodeInventorySnapshot(comp, *item)
		require.NoError(t, err)
		assert.Equal(t, resources, decoded)
		assert.Equal(t, original, item)
	})
}

func TestBackupInventoryNaming(t *testing.T) {
	comp := inventoryTestComposition()
	uuid := comp.Status.CurrentSynthesis.UUID
	hash := sha256.Sum256([]byte(comp.Namespace + "/" + comp.Spec.Synthesizer.Name))
	lineage := hex.EncodeToString(hash[:16])
	assert.Equal(t, lineage, inventoryLineage(comp))
	assert.Equal(t, "eno-inventory-"+lineage+"-"+uuid, inventoryName(comp, uuid))

	for _, tt := range []struct {
		name        string
		change      func(*apiv1.Composition)
		sameLineage bool
		sameName    bool
	}{
		{"replacement composition", func(c *apiv1.Composition) {
			c.Name, c.UID = "replacement", "new-uid"
			c.Labels, c.Annotations = nil, nil
		}, true, true},
		{"different namespace", func(c *apiv1.Composition) { c.Namespace = "other" }, false, false},
		{"different synthesizer", func(c *apiv1.Composition) { c.Spec.Synthesizer.Name = "other" }, false, false},
		{"different synthesis", func(c *apiv1.Composition) {
			c.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
		}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			other := comp.DeepCopy()
			tt.change(other)
			assert.Equal(t, tt.sameLineage, inventoryLineage(comp) == inventoryLineage(other))
			assert.Equal(t, tt.sameName, inventoryName(comp, uuid) == inventoryName(other, other.Status.CurrentSynthesis.UUID))
		})
	}
}

func TestBackupInventoryRejectsInvalid(t *testing.T) {
	comp := inventoryTestComposition()
	res := inventoryTestResource("desired")
	snapshot, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
	}})
	require.NoError(t, err)

	for _, tt := range []struct {
		name   string
		change func(*corev1.ConfigMap)
		err    string
	}{
		{"malformed JSON", func(cm *corev1.ConfigMap) { cm.Data[inventoryDataKey] = "{" }, "decoding inventory.json"},
		{"wrong ConfigMap namespace", func(cm *corev1.ConfigMap) {
			cm.Namespace = "other"
		}, `namespace "other" must be "kube-system"`},
		{"deleting ConfigMap", func(cm *corev1.ConfigMap) {
			now := metav1.Now()
			cm.DeletionTimestamp = &now
		}, "configmap is being deleted"},
		{"missing payload", func(cm *corev1.ConfigMap) {
			delete(cm.Data, inventoryDataKey)
		}, "missing data key"},
		{"missing metadata", func(cm *corev1.ConfigMap) {
			cm.Annotations = nil
		}, "does not match composition lineage"},
		{"wrong Composition namespace", func(cm *corev1.ConfigMap) {
			cm.Annotations[inventoryCompositionNamespaceAnnotation] = "other"
		}, "does not match composition lineage"},
		{"wrong metadata lineage", func(cm *corev1.ConfigMap) {
			cm.Annotations[inventorySynthesizerNameAnnotation] = "other"
		}, "does not match composition lineage"},
		{"wrong lineage label", func(cm *corev1.ConfigMap) {
			cm.Labels[inventoryLineageLabel] = "other"
		}, "does not match lineage"},
		{"UUID does not match name", func(cm *corev1.ConfigMap) {
			cm.Annotations[inventorySynthesisUUIDAnnotation] = "00000000-0000-4000-8000-000000000002"
		}, "must be"},
		{"old JSON envelope", func(cm *corev1.ConfigMap) {
			cm.Annotations = nil
			cm.Data[inventoryDataKey] = inventoryTestJSON(t, map[string]any{
				"formatVersion": 1, "compositionNamespace": comp.Namespace, "synthesizerName": comp.Spec.Synthesizer.Name,
				"synthesisUUID": comp.Status.CurrentSynthesis.UUID, "synthesized": comp.Status.CurrentSynthesis.Synthesized,
				"resources": []inventoryResource{res},
			})
		}, "decoding inventory.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := snapshot.DeepCopy()
			tt.change(item)
			got, err := decodeInventorySnapshot(comp, *item)
			require.ErrorContains(t, err, tt.err)
			var invalid *invalidInventoryError
			assert.ErrorAs(t, err, &invalid)
			if tt.name == "malformed JSON" {
				var syntax *json.SyntaxError
				assert.ErrorAs(t, err, &syntax)
			}
			assert.Nil(t, got)
			matches, err := inventoriesMatch(comp, item, snapshot)
			require.ErrorAs(t, err, &invalid)
			assert.False(t, matches)
		})
	}
}

func TestBackupInventoryComparison(t *testing.T) {
	comp := inventoryTestComposition()
	base, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, inventoryTestResource("desired"))}},
	}})
	require.NoError(t, err)
	for _, tt := range []struct {
		name   string
		change func(existing, intended *corev1.ConfigMap)
		err    string
	}{
		{"missing resources", func(existing, intended *corev1.ConfigMap) {
			existing.Data[inventoryDataKey] = "[]"
		}, ""},
		{"different format metadata", func(existing, intended *corev1.ConfigMap) {
			existing.Annotations[inventoryFormatVersionAnnotation] = "other"
		}, ""},
		{"different synthesis time", func(existing, intended *corev1.ConfigMap) {
			existing.Annotations[inventorySynthesizedAnnotation] = "2026-09-16T12:00:01Z"
		}, ""},
		{"invalid existing timestamp", func(existing, intended *corev1.ConfigMap) {
			existing.Annotations[inventorySynthesizedAnnotation] = "invalid"
		}, "invalid source synthesized timestamp"},
		{"invalid intended timestamp", func(existing, intended *corev1.ConfigMap) {
			intended.Annotations[inventorySynthesizedAnnotation] = "invalid"
		}, "invalid source synthesized timestamp"},
		{"invalid intended JSON", func(existing, intended *corev1.ConfigMap) {
			intended.Data[inventoryDataKey] = "{"
		}, "decoding inventory.json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			existing, intended := base.DeepCopy(), base.DeepCopy()
			tt.change(existing, intended)
			matches, err := inventoriesMatch(comp, existing, intended)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, matches)
		})
	}
}

func TestBackupInventorySizeLimit(t *testing.T) {
	const limit = 1 << 20
	comp := inventoryTestComposition()
	snapshot, err := makeInventory(comp, nil)
	require.NoError(t, err)
	for _, field := range []string{"data", "binaryData"} {
		t.Run(field, func(t *testing.T) {
			item := snapshot.DeepCopy()
			padding := strings.Repeat("x", limit-len(item.Data[inventoryDataKey]))
			if field == "data" {
				item.Data["padding"] = padding
			} else {
				item.BinaryData = map[string][]byte{"padding": []byte(padding)}
			}
			_, err := decodeInventorySnapshot(comp, *item)
			require.NoError(t, err, "exactly 1 MiB must be accepted")
			item.Data["extra"] = "x"
			got, err := decodeInventorySnapshot(comp, *item)
			require.NoError(t, err, "decoding must not recheck ConfigMap size")
			assert.Empty(t, got)
		})
	}

	t.Run("recording oversized inventory", func(t *testing.T) {
		labels := map[string]string{}
		for i := range 64 {
			labels[fmt.Sprintf("label-%d", i)] = strings.Repeat("x", 63)
		}
		var manifests []apiv1.Manifest
		var resources []inventoryResource
		for i := range 256 {
			res := inventoryTestResource(fmt.Sprintf("resource-%d", i))
			res.Labels = labels
			resources = append(resources, res)
			manifests = append(manifests, inventoryTestManifest(t, res))
		}
		require.Greater(t, len(inventoryTestJSON(t, resources)), limit)
		got, err := makeInventory(comp, []apiv1.ResourceSlice{{
			Spec: apiv1.ResourceSliceSpec{Resources: manifests},
		}})
		require.ErrorContains(t, err, "exceeding the 1048576-byte ConfigMap limit")
		assert.Nil(t, got)
	})
}

func TestBackupInventorySelection(t *testing.T) {
	comp := inventoryTestComposition()
	newest, err := makeInventory(comp, nil)
	require.NoError(t, err)
	newest.CreationTimestamp = *comp.Status.CurrentSynthesis.Synthesized
	oldComp := comp.DeepCopy()
	oldComp.Status.CurrentSynthesis.UUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	oldComp.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: comp.Status.CurrentSynthesis.Synthesized.Add(-time.Hour)}
	older, err := makeInventory(oldComp, nil)
	require.NoError(t, err)
	older.CreationTimestamp = metav1.NewTime(newest.CreationTimestamp.Add(time.Hour))
	matches, err := inventoriesMatch(comp, newest, older)
	require.NoError(t, err)
	assert.False(t, matches)
	tiedComp := comp.DeepCopy()
	tiedComp.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
	tied, err := makeInventory(tiedComp, nil)
	require.NoError(t, err)
	unparsed := newest.DeepCopy()
	unparsed.Data[inventoryDataKey] = "{"
	invalid := newest.DeepCopy()
	invalid.Annotations[inventorySynthesizedAnnotation] = "invalid"
	offset := older.DeepCopy()
	offset.Annotations[inventorySynthesizedAnnotation] = "2026-09-16T13:00:00+02:00"
	missingTime := newest.DeepCopy()
	delete(missingTime.Annotations, inventorySynthesizedAnnotation)
	zeroTime := newest.DeepCopy()
	zeroTime.Annotations[inventorySynthesizedAnnotation] = "0001-01-01T00:00:00Z"

	for _, tt := range []struct {
		name  string
		items []corev1.ConfigMap
		want  *corev1.ConfigMap
		err   string
	}{
		{"none", nil, nil, ""},
		{"single inventory", []corev1.ConfigMap{*newest}, newest, ""},
		{"source time overrides creation time and UUID", []corev1.ConfigMap{*newest, *older}, newest, ""},
		{"reverse order", []corev1.ConfigMap{*older, *newest}, newest, ""},
		{"equal timestamps keep first", []corev1.ConfigMap{*older, *tied, *newest}, tied, ""},
		{"does not decode resources", []corev1.ConfigMap{*older, *unparsed}, unparsed, ""},
		{"timestamps compare instants not strings", []corev1.ConfigMap{*offset, *newest}, newest, ""},
		{"invalid timestamp", []corev1.ConfigMap{*newest, *invalid}, nil, "invalid source synthesized timestamp"},
		{"missing timestamp", []corev1.ConfigMap{*missingTime}, nil, "invalid source synthesized timestamp"},
		{"zero timestamp", []corev1.ConfigMap{*zeroTime}, nil, "missing or zero source synthesized timestamp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectInventory(tt.items)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				var invalid *invalidInventoryError
				assert.ErrorAs(t, err, &invalid)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("returned ConfigMap is independent", func(t *testing.T) {
		items := []corev1.ConfigMap{*newest.DeepCopy()}
		selected, err := selectInventory(items)
		require.NoError(t, err)
		selected.Annotations[inventorySynthesizedAnnotation] = "changed"
		selected.Data[inventoryDataKey] = "changed"
		assert.Equal(t, *newest, items[0])
	})
}

func TestBackupInventoryMakeResources(t *testing.T) {
	comp := inventoryTestComposition()
	first, second := inventoryTestResource("desired"), inventoryTestResource("desired")
	first.Annotations["example.com/ignored"] = "not inventoried"
	first.Labels["variant"], second.Labels["variant"] = "first", "second"
	second.Version = "v1beta1"
	otherNamespace := inventoryTestResource("desired")
	otherNamespace.Namespace = "other"
	additional := inventoryTestResource("additional")
	otherKind := inventoryTestResource("desired")
	otherKind.Kind = "StatefulSet"
	otherGroup := inventoryTestResource("desired")
	otherGroup.Group = "extensions"
	clusterScoped := inventoryResource{
		Version: "v1", Kind: "Namespace", Name: "desired",
		Labels: map[string]string{}, Annotations: map[string]string{},
	}
	deleted := inventoryTestManifest(t, inventoryTestResource("deleted"))
	deleted.Deleted = true
	slice := apiv1.ResourceSlice{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{
		inventoryTestManifest(t, first), inventoryTestManifest(t, second),
		inventoryTestManifest(t, otherNamespace), inventoryTestManifest(t, additional),
		inventoryTestManifest(t, otherKind), inventoryTestManifest(t, otherGroup), inventoryTestManifest(t, clusterScoped),
		deleted, {Manifest: "{", Deleted: true}, {Manifest: inventoryTestPatch},
	}}}
	delete(first.Annotations, "example.com/ignored")
	clusterScoped.Labels, clusterScoped.Annotations = nil, nil
	for _, order := range []string{"original", "reversed"} {
		t.Run(order, func(t *testing.T) {
			input := slice.DeepCopy()
			winner := first
			if order == "reversed" {
				slices.Reverse(input.Spec.Resources)
				winner = second
			}
			snapshot, err := makeInventory(comp, []apiv1.ResourceSlice{
				{Spec: apiv1.ResourceSliceSpec{Resources: input.Spec.Resources[:1]}},
				{Spec: apiv1.ResourceSliceSpec{Resources: input.Spec.Resources[1:]}},
			})
			require.NoError(t, err)
			resources, err := decodeInventorySnapshot(comp, *snapshot)
			require.NoError(t, err)
			assert.Equal(t, []inventoryResource{clusterScoped, otherNamespace, additional, winner, otherKind, otherGroup}, resources)
		})
	}

	t.Run("invalid live manifest", func(t *testing.T) {
		_, err := makeInventory(comp, []apiv1.ResourceSlice{{
			Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{Manifest: "{"}}},
		}})
		require.ErrorContains(t, err, "invalid manifest JSON")
	})
}

func TestBackupInventoryMissingTombstones(t *testing.T) {
	comp := inventoryTestComposition()
	desired := inventoryTestResource("desired")
	missing := inventoryTestResource("missing")
	deleted := inventoryTestResource("deleted")
	patched := inventoryTestResource("patched")
	additional := inventoryTestResource("additional")
	additional.Labels["variant"] = "other"
	var history []apiv1.Manifest
	for _, res := range []inventoryResource{desired, missing, deleted, patched, additional} {
		history = append(history, inventoryTestManifest(t, res))
	}
	snapshot, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: history},
	}})
	require.NoError(t, err)
	resources, err := decodeInventorySnapshot(comp, *snapshot)
	require.NoError(t, err)

	desired.Version = "v1beta1"
	desired.Labels["app"] = "updated"
	tombstone := inventoryTestManifest(t, deleted)
	tombstone.Deleted = true
	current := apiv1.ResourceSlice{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{
		inventoryTestManifest(t, desired), tombstone, {Manifest: inventoryTestPatch},
	}}}
	tombstones, err := missingTombstones([]apiv1.ResourceSlice{current}, resources)
	require.NoError(t, err)
	require.Len(t, tombstones, 3)
	for i, expected := range []inventoryResource{additional, missing, patched} {
		assert.True(t, tombstones[i].Deleted)
		assert.JSONEq(t, inventoryTestManifest(t, expected).Manifest, tombstones[i].Manifest)
	}
	require.Len(t, current.Spec.Resources, 3)
	current.Spec.Resources = append(current.Spec.Resources, tombstones...)
	repeated, err := missingTombstones([]apiv1.ResourceSlice{current}, resources)
	require.NoError(t, err)
	assert.Empty(t, repeated)
}

func TestBackupInventoryMissingTombstonesPreservesEntries(t *testing.T) {
	first, second := inventoryTestResource("second"), inventoryTestResource("first")
	clusterScoped := inventoryResource{Version: "v1", Kind: "Namespace", Name: "cluster"}
	duplicate := second
	duplicate.Version = "v1beta1"
	resources := []inventoryResource{first, clusterScoped, second, duplicate}
	before := inventoryTestJSON(t, resources)
	tombstones, err := missingTombstones(nil, resources)
	require.NoError(t, err)
	require.Len(t, tombstones, 3)
	for i, expected := range []string{
		inventoryTestManifest(t, first).Manifest,
		`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"cluster"}}`,
		inventoryTestManifest(t, second).Manifest,
	} {
		assert.True(t, tombstones[i].Deleted)
		assert.JSONEq(t, expected, tombstones[i].Manifest)
	}
	assert.Equal(t, before, inventoryTestJSON(t, resources))

	empty, err := missingTombstones(nil, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestBackupInventoryMissingTombstonesErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		manifest string
		err      string
	}{
		{"invalid slice JSON", "{", "invalid manifest JSON"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			current := []apiv1.ResourceSlice{{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{Manifest: tt.manifest}}}}}
			tombstones, err := missingTombstones(current, []inventoryResource{inventoryTestResource("missing")})
			require.ErrorContains(t, err, tt.err)
			assert.Nil(t, tombstones)
		})
	}
}
