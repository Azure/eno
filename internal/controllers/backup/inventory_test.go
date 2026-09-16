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
	enocel "github.com/Azure/eno/internal/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const inventoryTestPatch = `{"apiVersion":"eno.azure.io/v1","kind":"Patch","metadata":{"name":"patched","namespace":"workloads"},"patch":{"apiVersion":"apps/v1beta1","kind":"Deployment","ops":[]}}`

func inventoryTestComposition() *apiv1.Composition {
	synthesized := metav1.NewTime(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "composition", Namespace: "control", UID: "composition-uid",
			Labels:      map[string]string{"owner": "team"},
			Annotations: map[string]string{"example.com/source": "saved"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				&apiv1.Symphony{ObjectMeta: metav1.ObjectMeta{Name: "symphony", UID: "symphony-uid"}},
				apiv1.SchemeGroupVersion.WithKind("Symphony"),
			)},
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
	snapshot, err := makeInventory(t.Context(), comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
	}}, nil)
	require.NoError(t, err)
	assert.Equal(t, inventory{
		FormatVersion: 1, CompositionNamespace: comp.Namespace, SynthesizerName: comp.Spec.Synthesizer.Name,
		SynthesisUUID: comp.Status.CurrentSynthesis.UUID, Synthesized: *comp.Status.CurrentSynthesis.Synthesized,
		Resources: []inventoryResource{res},
		SourceComposition: &inventorySourceComposition{
			Name: comp.Name, UID: comp.UID, Labels: comp.Labels, Annotations: comp.Annotations,
			Symphony: &inventorySourceSymphony{Name: "symphony", UID: "symphony-uid"},
		},
	}, snapshot.Data)
	assert.Equal(t, "kube-system", snapshot.Object.Namespace)

	recreated := comp.DeepCopy()
	recreated.Name, recreated.UID = "replacement", "replacement-uid"
	recreated.Labels, recreated.Annotations, recreated.OwnerReferences = nil, nil, nil
	recovered, err := decodeInventory(recreated, snapshot.Object)
	require.NoError(t, err)
	assert.Equal(t, snapshot.Data, recovered.Data)
	assert.True(t, inventoriesMatch(recovered, snapshot))

	cleanupSnapshot, err := decodeInventoryForCleanup(snapshot.Object)
	require.NoError(t, err)
	cleanup, err := cleanupSnapshot.cleanupComposition()
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	assert.Equal(t, comp.ObjectMeta, cleanup.ObjectMeta)
	assert.Equal(t, comp.Spec, cleanup.Spec)
	cleanup.Labels["owner"] = "changed"
	cleanup.Annotations["example.com/source"] = "changed"
	assert.Equal(t, "team", cleanupSnapshot.Data.SourceComposition.Labels["owner"])
	assert.Equal(t, "saved", cleanupSnapshot.Data.SourceComposition.Annotations["example.com/source"])

	for _, provenance := range []string{"legacy", "composition-only"} {
		t.Run(provenance, func(t *testing.T) {
			data := snapshot.Data
			if provenance == "legacy" {
				data.SourceComposition = nil
			} else {
				source := *data.SourceComposition
				source.Symphony = nil
				data.SourceComposition = &source
			}
			item := snapshot.Object.DeepCopy()
			item.Data[inventoryDataKey] = inventoryTestJSON(t, data)
			recovered, err := selectInventory(recreated, []corev1.ConfigMap{*item})
			require.NoError(t, err)
			require.NotNil(t, recovered)
			assert.Equal(t, data, recovered.Data)
			if provenance == "legacy" {
				assert.True(t, inventoriesMatch(recovered, snapshot))
			}
			cleanupSnapshot, err := decodeInventoryForCleanup(*item)
			require.NoError(t, err)
			cleanup, err := cleanupSnapshot.cleanupComposition()
			require.NoError(t, err)
			assert.Nil(t, cleanup)
		})
	}

	t.Run("empty inventory", func(t *testing.T) {
		empty, err := makeInventory(t.Context(), comp, nil, nil)
		require.NoError(t, err)
		decoded, err := decodeInventory(comp, empty.Object)
		require.NoError(t, err)
		assert.NotNil(t, decoded.Data.Resources)
		assert.Empty(t, decoded.Data.Resources)
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
			c.Labels, c.Annotations, c.OwnerReferences = nil, nil, nil
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
	snapshot, err := makeInventory(t.Context(), comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
	}}, nil)
	require.NoError(t, err)

	for _, tt := range []struct {
		name   string
		change func(*corev1.ConfigMap)
		err    string
	}{
		{"malformed JSON", func(cm *corev1.ConfigMap) { cm.Data[inventoryDataKey] = "{" }, "decoding inventory.json"},
		{"unsupported version", func(cm *corev1.ConfigMap) {
			cm.Data[inventoryDataKey] = strings.Replace(cm.Data[inventoryDataKey], `"formatVersion":1`, `"formatVersion":2`, 1)
		}, "unsupported formatVersion"},
		{"wrong payload lineage", func(cm *corev1.ConfigMap) {
			data := snapshot.Data
			data.SynthesizerName = "other"
			cm.Data[inventoryDataKey] = inventoryTestJSON(t, data)
		}, "does not match composition lineage"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := snapshot.Object.DeepCopy()
			tt.change(item)
			got, err := decodeInventory(comp, *item)
			require.ErrorContains(t, err, tt.err)
			var invalid *invalidInventoryError
			assert.ErrorAs(t, err, &invalid)
			assert.Nil(t, got)
		})
	}
}

func TestBackupInventorySizeLimit(t *testing.T) {
	const limit = 1 << 20
	comp := inventoryTestComposition()
	snapshot, err := makeInventory(t.Context(), comp, nil, nil)
	require.NoError(t, err)
	for _, field := range []string{"data", "binaryData"} {
		t.Run(field, func(t *testing.T) {
			item := snapshot.Object.DeepCopy()
			padding := strings.Repeat("x", limit-len(item.Data[inventoryDataKey]))
			if field == "data" {
				item.Data["padding"] = padding
			} else {
				item.BinaryData = map[string][]byte{"padding": []byte(padding)}
			}
			_, err := decodeInventory(comp, *item)
			require.NoError(t, err, "exactly 1 MiB must be accepted")
			item.Data["extra"] = "x"
			got, err := decodeInventory(comp, *item)
			require.ErrorContains(t, err, "1048577 bytes, exceeding the 1048576-byte limit")
			assert.Nil(t, got)
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
		data := snapshot.Data
		data.Resources = resources
		require.Greater(t, len(inventoryTestJSON(t, data)), limit)
		got, err := makeInventory(t.Context(), comp, []apiv1.ResourceSlice{{
			Spec: apiv1.ResourceSliceSpec{Resources: manifests},
		}}, nil)
		require.ErrorContains(t, err, "exceeding the 1048576-byte ConfigMap limit")
		assert.Nil(t, got)
	})
}

func TestBackupInventorySelection(t *testing.T) {
	comp := inventoryTestComposition()
	newest, err := makeInventory(t.Context(), comp, nil, nil)
	require.NoError(t, err)
	newest.Object.CreationTimestamp = *comp.Status.CurrentSynthesis.Synthesized
	oldComp := comp.DeepCopy()
	oldComp.Status.CurrentSynthesis.UUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	oldComp.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: newest.Data.Synthesized.Add(-time.Hour)}
	older, err := makeInventory(t.Context(), oldComp, nil, nil)
	require.NoError(t, err)
	older.Object.CreationTimestamp = metav1.NewTime(newest.Object.CreationTimestamp.Add(time.Hour))
	tiedComp := comp.DeepCopy()
	tiedComp.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
	tied, err := makeInventory(t.Context(), tiedComp, nil, nil)
	require.NoError(t, err)
	conflict := newest.Object.DeepCopy()
	conflictingData := newest.Data
	conflictingData.Resources = []inventoryResource{inventoryTestResource("conflict")}
	conflict.Data[inventoryDataKey] = inventoryTestJSON(t, conflictingData)

	for _, tt := range []struct {
		name  string
		items []corev1.ConfigMap
		err   string
	}{
		{"none", nil, ""},
		{"source time overrides creation time and UUID", []corev1.ConfigMap{newest.Object, older.Object}, ""},
		{"reverse order", []corev1.ConfigMap{older.Object, newest.Object}, ""},
		{"identical duplicate", []corev1.ConfigMap{newest.Object, newest.Object}, ""},
		{"ambiguous newest", []corev1.ConfigMap{older.Object, newest.Object, tied.Object}, "share greatest source synthesized timestamp"},
		{"conflicting same UUID", []corev1.ConfigMap{newest.Object, *conflict}, "conflicting snapshots for synthesis"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectInventory(comp, tt.items)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				var invalid *invalidInventoryError
				assert.ErrorAs(t, err, &invalid)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if len(tt.items) == 0 {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, newest.Data, got.Data)
			}
		})
	}
}

func TestBackupInventoryMakeResources(t *testing.T) {
	comp := inventoryTestComposition()
	first, second := inventoryTestResource("desired"), inventoryTestResource("desired")
	first.Annotations["example.com/ignored"] = "not inventoried"
	otherNamespace := inventoryTestResource("desired")
	otherNamespace.Namespace = "other"
	excluded := inventoryTestResource("excluded")
	excluded.Labels["owner"] = "another-team"
	deleted := inventoryTestManifest(t, inventoryTestResource("deleted"))
	deleted.Deleted = true
	slice := apiv1.ResourceSlice{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{
		inventoryTestManifest(t, first), inventoryTestManifest(t, second),
		inventoryTestManifest(t, otherNamespace), inventoryTestManifest(t, excluded),
		deleted, {Manifest: inventoryTestPatch},
	}}}
	filter, err := enocel.Parse(`self.metadata.labels.owner == composition.metadata.labels.owner`)
	require.NoError(t, err)
	for _, order := range []string{"original", "reversed"} {
		t.Run(order, func(t *testing.T) {
			input := slice.DeepCopy()
			if order == "reversed" {
				slices.Reverse(input.Spec.Resources)
			}
			snapshot, err := makeInventory(t.Context(), comp, []apiv1.ResourceSlice{*input}, filter)
			require.NoError(t, err)
			assert.ElementsMatch(t, []inventoryResource{otherNamespace, inventoryTestResource("desired")}, snapshot.Data.Resources)
		})
	}
}

func TestBackupInventoryMissingTombstones(t *testing.T) {
	comp := inventoryTestComposition()
	desired := inventoryTestResource("desired")
	missing := inventoryTestResource("missing")
	deleted := inventoryTestResource("deleted")
	patched := inventoryTestResource("patched")
	excluded := inventoryTestResource("excluded")
	excluded.Labels["owner"] = "another-team"
	var history []apiv1.Manifest
	for _, res := range []inventoryResource{desired, missing, deleted, patched, excluded} {
		history = append(history, inventoryTestManifest(t, res))
	}
	snapshot, err := makeInventory(t.Context(), comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: history},
	}}, nil)
	require.NoError(t, err)

	desired.Version = "v1beta1"
	desired.Labels["owner"] = "another-team"
	tombstone := inventoryTestManifest(t, deleted)
	tombstone.Deleted = true
	current := apiv1.ResourceSlice{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{
		inventoryTestManifest(t, desired), tombstone, {Manifest: inventoryTestPatch},
	}}}
	filter, err := enocel.Parse(`self.metadata.labels.owner == composition.metadata.labels.owner`)
	require.NoError(t, err)
	tombstones, err := missingTombstones(t.Context(), comp, []apiv1.ResourceSlice{current}, snapshot, filter)
	require.NoError(t, err)
	require.Len(t, tombstones, 1)
	assert.True(t, tombstones[0].Deleted)
	assert.JSONEq(t, inventoryTestManifest(t, missing).Manifest, tombstones[0].Manifest)
	require.Len(t, current.Spec.Resources, 3)
	current.Spec.Resources = append(current.Spec.Resources, tombstones...)
	repeated, err := missingTombstones(t.Context(), comp, []apiv1.ResourceSlice{current}, snapshot, filter)
	require.NoError(t, err)
	assert.Empty(t, repeated)
}
