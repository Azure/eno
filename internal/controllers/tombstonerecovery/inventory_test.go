package tombstonerecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
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

func inventoryTestChunkedSnapshot(t *testing.T, comp *apiv1.Composition) []corev1.Secret {
	t.Helper()
	var manifests []apiv1.Manifest
	for _, name := range []string{"first", "second", "third"} {
		res := inventoryTestResource(name)
		res.Labels = map[string]string{"padding": strings.Repeat("x", inventoryMaxDataBytes/2)}
		manifests = append(manifests, inventoryTestManifest(t, res))
	}
	chunks, err := makeInventory(comp, []apiv1.ResourceSlice{{Spec: apiv1.ResourceSliceSpec{Resources: manifests}}})
	require.NoError(t, err)
	require.Len(t, chunks, 3)
	return chunks
}

func TestTombstoneRecoveryInventoryChunkValidation(t *testing.T) {
	comp := inventoryTestComposition()
	base := inventoryTestChunkedSnapshot(t, comp)
	for _, mode := range []string{"reordered", "missing", "duplicate index", "mixed timestamp", "mixed count"} {
		t.Run(mode, func(t *testing.T) {
			chunks := (&corev1.SecretList{Items: base}).DeepCopy().Items
			switch mode {
			case "reordered":
				slices.Reverse(chunks)
			case "missing":
				chunks = chunks[:2]
			case "duplicate index":
				chunks[1] = *chunks[0].DeepCopy()
			case "mixed timestamp":
				chunks[1].Annotations[inventorySynthesizedAnnotation] = comp.Status.CurrentSynthesis.Synthesized.Add(time.Second).Format(time.RFC3339)
			case "mixed count":
				chunks[1].Annotations[inventoryChunkCountAnnotation] = "4"
			}
			before := (&corev1.SecretList{Items: chunks}).DeepCopy()
			decoded, err := loadInventoryForTest(chunks)
			if mode == "reordered" {
				require.NoError(t, err)
				require.Len(t, decoded, 3)
				assert.Equal(t, []string{"first", "second", "third"}, []string{decoded[0].Name, decoded[1].Name, decoded[2].Name})
			} else {
				require.Error(t, err)
				assert.Nil(t, decoded)
			}
			assert.Equal(t, before.Items, chunks)
		})
	}
	t.Run("ignore incomplete newer upload", func(t *testing.T) {
		old := comp.DeepCopy()
		old.Status.CurrentSynthesis.UUID = "old"
		old.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: comp.Status.CurrentSynthesis.Synthesized.Add(-time.Minute)}
		older := inventoryTestChunkedSnapshot(t, old)
		items := append([]corev1.Secret{base[0]}, older...)
		selected, err := selectInventory(items)
		require.NoError(t, err)
		require.Len(t, selected, 3)
		assert.Equal(t, old.Status.CurrentSynthesis.UUID, selected[0].Labels[inventorySynthesisUUIDLabel])
		_, err = decodeInventory(selected)
		require.NoError(t, err)
	})
	t.Run("ignore old extra chunks after shrink", func(t *testing.T) {
		next := comp.DeepCopy()
		next.Status.CurrentSynthesis.UUID = "next"
		next.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: comp.Status.CurrentSynthesis.Synthesized.Add(time.Minute)}
		replacement, err := makeInventory(next, nil)
		require.NoError(t, err)
		items := append(replacement, base[1:]...)
		selected, err := selectInventory(items)
		require.NoError(t, err)
		require.Len(t, selected, 1)
		decoded, err := decodeInventory(selected)
		require.NoError(t, err)
		assert.Empty(t, decoded)
	})
}

func TestTombstoneRecoveryInventoryManifestValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		metadata string
		wantError string
	}{
		{name: "absent"},
		{name: "null", metadata: `,"labels":null,"annotations":null`},
		{name: "empty maps", metadata: `,"labels":{},"annotations":{}`},
		{name: "invalid labels", metadata: `,"labels":{"key":42}`, wantError: "invalid manifest labels"},
		{name: "invalid annotations", metadata: `,"annotations":[]`, wantError: "invalid manifest annotations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := apiv1.Manifest{Manifest: `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"desired"` + tc.metadata + `}}`}
			got, err := makeInventory(inventoryTestComposition(), []apiv1.ResourceSlice{{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{manifest}}}})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				assert.Nil(t, got)
			} else {
				require.NoError(t, err)
				decoded, err := loadInventoryForTest(got)
				require.NoError(t, err)
				require.Len(t, decoded, 1)
				assert.Nil(t, decoded[0].Labels)
				assert.Nil(t, decoded[0].Annotations)
			}
		})
	}
	for _, deleted := range []bool{false, true} {
		_, err := makeInventory(inventoryTestComposition(), []apiv1.ResourceSlice{{
			Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{{Manifest: "{", Deleted: deleted}}},
		}})
		require.ErrorContains(t, err, "invalid manifest JSON")
	}
}

func TestTombstoneRecoveryInventoryRoundTrip(t *testing.T) {
	comp := inventoryTestComposition()
	res := inventoryTestResource("desired")
	inventorySecrets, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
	}})
	require.NoError(t, err)
	require.Len(t, inventorySecrets, 1)
	item := &inventorySecrets[0]
	assert.JSONEq(t, inventoryTestJSON(t, []inventoryResource{res}), string(item.Data[inventoryDataKey]))
	assert.Equal(t, map[string]string{
		inventorySynthesizedAnnotation: "2026-09-16T12:00:00Z",
		inventoryChunkIndexAnnotation:  "0",
		inventoryChunkCountAnnotation:  "1",
	}, item.Annotations)
	assert.Equal(t, map[string]string{
		inventoryLineageLabel:       inventoryLineage(comp),
		inventorySynthesisUUIDLabel: comp.Status.CurrentSynthesis.UUID,
	}, item.Labels)
	assert.Equal(t, corev1.SecretTypeOpaque, item.Type)
	assert.Equal(t, "kube-system", item.Namespace)

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	require.NoError(t, cli.Create(t.Context(), item))
	stored := &corev1.Secret{}
	require.NoError(t, cli.Get(t.Context(), client.ObjectKeyFromObject(item), stored))
	assert.Equal(t, item.Data, stored.Data)

	recreated := comp.DeepCopy()
	recreated.Name, recreated.UID = "replacement", "replacement-uid"
	recreated.Labels, recreated.Annotations = nil, nil
	recreated.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
	recovered, err := loadInventoryForTest([]corev1.Secret{*stored})
	require.NoError(t, err)
	assert.Equal(t, []inventoryResource{res}, recovered)

	t.Run("empty inventory", func(t *testing.T) {
		empty, err := makeInventory(comp, nil)
		require.NoError(t, err)
		require.Len(t, empty, 1)
		decoded, err := loadInventoryForTest(empty)
		require.NoError(t, err)
		assert.NotNil(t, decoded)
		assert.Empty(t, decoded)
		assert.Equal(t, "[]", string(empty[0].Data[inventoryDataKey]))
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
		require.Len(t, built, 1)
		item := built[0].DeepCopy()
		assert.Equal(t, "2026-09-16T12:00:00Z", item.Annotations[inventorySynthesizedAnnotation])
		item.Annotations[inventorySynthesizedAnnotation] = comp.Status.CurrentSynthesis.Synthesized.Format(time.RFC3339)
		original := item.DeepCopy()
		decoded, err := loadInventoryForTest([]corev1.Secret{*item})
		require.NoError(t, err)
		assert.Len(t, decoded, 1)
		assert.Equal(t, original, item)
	})

	t.Run("decoding preserves resource entries", func(t *testing.T) {
		item := inventorySecrets[0].DeepCopy()
		resources := []inventoryResource{inventoryTestResource("second"), inventoryTestResource("first")}
		resources[0].Labels = map[string]string{}
		item.Data[inventoryDataKey] = []byte(strings.Replace(inventoryTestJSON(t, resources), `"name":"second"`, `"name":"second","labels":{}`, 1))
		original := item.DeepCopy()
		decoded, err := loadInventoryForTest([]corev1.Secret{*item})
		require.NoError(t, err)
		assert.Equal(t, resources, decoded)
		assert.Equal(t, original, item)
	})
}

func TestTombstoneRecoveryInventoryNaming(t *testing.T) {
	comp := inventoryTestComposition()
	hash := sha256.Sum256([]byte(comp.Namespace + "/" + comp.Spec.Synthesizer.Name))
	lineage := hex.EncodeToString(hash[:16])
	assert.Equal(t, lineage, inventoryLineage(comp))
	assert.Equal(t, "eno-inventory-"+lineage+"-"+comp.Status.CurrentSynthesis.UUID+"-0",
		inventoryName(lineage, comp.Status.CurrentSynthesis.UUID, 0))

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
		{"different namespace", func(c *apiv1.Composition) {
			c.Namespace = "other"
		}, false, false},
		{"different synthesizer", func(c *apiv1.Composition) {
			c.Spec.Synthesizer.Name = "other"
		}, false, false},
		{"different synthesis", func(c *apiv1.Composition) {
			c.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
		}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			other := comp.DeepCopy()
			tt.change(other)
			assert.Equal(t, tt.sameLineage, inventoryLineage(comp) == inventoryLineage(other))
			assert.Equal(t, tt.sameName,
				inventoryName(lineage, comp.Status.CurrentSynthesis.UUID, 0) ==
					inventoryName(inventoryLineage(other), other.Status.CurrentSynthesis.UUID, 0))
		})
	}
	for _, index := range []int{0, 10, 1000} {
		got := inventoryName(lineage, comp.Status.CurrentSynthesis.UUID, index)
		assert.True(t, strings.HasSuffix(got, "-"+strconv.Itoa(index)))
	}
}

func TestTombstoneRecoveryInventoryRejectsInvalid(t *testing.T) {
	comp := inventoryTestComposition()
	res := inventoryTestResource("desired")
	inventorySecrets, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, res)}},
	}})
	require.NoError(t, err)

	for _, tt := range []struct {
		name   string
		change func(*corev1.Secret)
		err    string
	}{
		{"malformed JSON", func(cm *corev1.Secret) {
			cm.Data[inventoryDataKey] = []byte("{")
		}, "decoding Secret"},
		{"null payload", func(cm *corev1.Secret) {
			cm.Data[inventoryDataKey] = []byte("null")
		}, "must be a JSON array"},
		{"missing payload", func(cm *corev1.Secret) {
			delete(cm.Data, inventoryDataKey)
		}, "missing data key"},
		{"missing metadata", func(cm *corev1.Secret) {
			cm.Annotations = nil
		}, "invalid source synthesized timestamp"},
		{"negative index", func(cm *corev1.Secret) {
			cm.Annotations[inventoryChunkIndexAnnotation] = "-1"
		}, "no complete inventory snapshot found"},
		{"index outside count", func(cm *corev1.Secret) {
			cm.Annotations[inventoryChunkIndexAnnotation] = "1"
		}, "no complete inventory snapshot found"},
		{"zero count", func(cm *corev1.Secret) {
			cm.Annotations[inventoryChunkCountAnnotation] = "0"
		}, "no complete inventory snapshot found"},
		{"missing resource name", func(cm *corev1.Secret) {
			cm.Data[inventoryDataKey] = []byte(`[{"version":"v1","kind":"ConfigMap"}]`)
		}, "resource identity requires"},
		{"Patch resource", func(cm *corev1.Secret) {
			cm.Data[inventoryDataKey] = []byte(`[{"group":"eno.azure.io","version":"v1","kind":"Patch","name":"patch"}]`)
		}, "Patch pseudo-resource"},
		{"old JSON envelope", func(cm *corev1.Secret) {
			cm.Data[inventoryDataKey] = []byte(inventoryTestJSON(t, map[string]any{
				"formatVersion": 1, "compositionNamespace": comp.Namespace, "synthesizerName": comp.Spec.Synthesizer.Name,
				"synthesisUUID": comp.Status.CurrentSynthesis.UUID, "synthesized": comp.Status.CurrentSynthesis.Synthesized,
				"resources": []inventoryResource{res},
			}))
		}, "decoding Secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := inventorySecrets[0].DeepCopy()
			tt.change(item)
			got, err := loadInventoryForTest([]corev1.Secret{*item})
			require.ErrorContains(t, err, tt.err)
			if tt.name == "malformed JSON" {
				var syntax *json.SyntaxError
				assert.ErrorAs(t, err, &syntax)
			}
			assert.Nil(t, got)
		})
	}
}

func TestTombstoneRecoveryInventorySizeLimit(t *testing.T) {
	const limit = 1 << 20
	comp := inventoryTestComposition()
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
		require.NoError(t, err)
		require.Greater(t, len(got), 1)
		for i, chunk := range got {
			assert.LessOrEqual(t, len(chunk.Data[inventoryDataKey]), limit)
			assert.True(t, json.Valid(chunk.Data[inventoryDataKey]))
			assert.Equal(t, strconv.Itoa(i), chunk.Annotations[inventoryChunkIndexAnnotation])
			assert.Equal(t, strconv.Itoa(len(got)), chunk.Annotations[inventoryChunkCountAnnotation])
		}
		decoded, err := loadInventoryForTest(got)
		require.NoError(t, err)
		assert.ElementsMatch(t, resources, decoded)
		before := inventoryTestJSON(t, manifests)
		retry, err := makeInventory(comp, []apiv1.ResourceSlice{{Spec: apiv1.ResourceSliceSpec{Resources: manifests}}})
		require.NoError(t, err)
		assert.Equal(t, got, retry)
		assert.Equal(t, before, inventoryTestJSON(t, manifests))
		slices.Reverse(manifests)
		reordered, err := makeInventory(comp, []apiv1.ResourceSlice{{Spec: apiv1.ResourceSliceSpec{Resources: manifests}}})
		require.NoError(t, err)
		assert.Equal(t, got, reordered, "input order must not change chunk boundaries")
	})

	t.Run("exact boundary and oversized entry", func(t *testing.T) {
		res := inventoryTestResource("large")
		res.Labels = map[string]string{"padding": ""}
		res.Labels["padding"] = strings.Repeat("x", limit-len(inventoryTestJSON(t, []inventoryResource{res})))
		chunks, err := packInventory([]inventoryResource{res})
		require.NoError(t, err)
		require.Len(t, chunks, 1)
		assert.Len(t, chunks[0], limit)
		chunks, err = packInventory([]inventoryResource{res, inventoryTestResource("next")})
		require.NoError(t, err)
		require.Len(t, chunks, 2)
		assert.Len(t, chunks[0], limit)
		res.Labels["padding"] += "x"
		chunks, err = packInventory([]inventoryResource{res})
		require.ErrorContains(t, err, "exceeds the 1048576-byte Secret data limit")
		assert.Nil(t, chunks)
	})
}

func TestTombstoneRecoveryInventorySelection(t *testing.T) {
	comp := inventoryTestComposition()
	inventorySecrets, err := makeInventory(comp, nil)
	require.NoError(t, err)
	newest := &inventorySecrets[0]
	newest.CreationTimestamp = *comp.Status.CurrentSynthesis.Synthesized
	oldComp := comp.DeepCopy()
	oldComp.Status.CurrentSynthesis.UUID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	oldComp.Status.CurrentSynthesis.Synthesized = &metav1.Time{Time: comp.Status.CurrentSynthesis.Synthesized.Add(-time.Hour)}
	oldSnapshot, err := makeInventory(oldComp, nil)
	require.NoError(t, err)
	older := &oldSnapshot[0]
	older.CreationTimestamp = metav1.NewTime(newest.CreationTimestamp.Add(time.Hour))
	tiedComp := comp.DeepCopy()
	tiedComp.Status.CurrentSynthesis.UUID = "00000000-0000-4000-8000-000000000002"
	tiedSnapshot, err := makeInventory(tiedComp, nil)
	require.NoError(t, err)
	tied := &tiedSnapshot[0]
	unparsed := newest.DeepCopy()
	unparsed.Data[inventoryDataKey] = []byte("{")
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
		items []corev1.Secret
		want  *corev1.Secret
		err   string
	}{
		{"none", nil, nil, "no complete inventory snapshot found"},
		{"single inventory", []corev1.Secret{*newest}, newest, ""},
		{"source time overrides creation time and UUID", []corev1.Secret{*newest, *older}, newest, ""},
		{"reverse order", []corev1.Secret{*older, *newest}, newest, ""},
		{"equal timestamps are ambiguous", []corev1.Secret{*older, *tied, *newest}, nil, "different synthesis UUIDs share"},
		{"reverse tie is also ambiguous", []corev1.Secret{*newest, *tied}, nil, "different synthesis UUIDs share"},
		{"does not decode resources", []corev1.Secret{*older, *unparsed}, unparsed, ""},
		{"timestamps compare instants not strings", []corev1.Secret{*offset, *newest}, newest, ""},
		{"invalid timestamp", []corev1.Secret{*newest, *invalid}, nil, "invalid source synthesized timestamp"},
		{"missing timestamp", []corev1.Secret{*missingTime}, nil, "invalid source synthesized timestamp"},
		{"zero timestamp", []corev1.Secret{*zeroTime}, nil, "missing or zero source synthesized timestamp"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectInventory(tt.items)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.want == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, []corev1.Secret{*tt.want}, got)
			}
		})
	}
}

func TestTombstoneRecoveryInventoryMakeResources(t *testing.T) {
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
		deleted, {Manifest: inventoryTestPatch},
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
			inventorySecrets, err := makeInventory(comp, []apiv1.ResourceSlice{
				{Spec: apiv1.ResourceSliceSpec{Resources: input.Spec.Resources[:1]}},
				{Spec: apiv1.ResourceSliceSpec{Resources: input.Spec.Resources[1:]}},
			})
			require.NoError(t, err)
			resources, err := loadInventoryForTest(inventorySecrets)
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

func TestTombstoneRecoveryInventoryMissingTombstones(t *testing.T) {
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
	inventorySecrets, err := makeInventory(comp, []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{Resources: history},
	}})
	require.NoError(t, err)
	resources, err := loadInventoryForTest(inventorySecrets)
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
	require.Len(t, tombstones, 2)
	for i, expected := range []inventoryResource{additional, missing} {
		assert.True(t, tombstones[i].Deleted)
		assert.JSONEq(t, inventoryTestManifest(t, expected).Manifest, tombstones[i].Manifest)
	}
	require.Len(t, current.Spec.Resources, 3)
	current.Spec.Resources = append(current.Spec.Resources, tombstones...)
	repeated, err := missingTombstones([]apiv1.ResourceSlice{current}, resources)
	require.NoError(t, err)
	assert.Empty(t, repeated)
}

func TestTombstoneRecoveryInventoryPatchTargetPresence(t *testing.T) {
	for _, target := range []inventoryResource{
		{Version: "v1", Kind: "ConfigMap", Namespace: "workloads", Name: "foo"},
		{Group: "apps", Version: "v1", Kind: "Deployment", Namespace: "workloads", Name: "foo"},
		{Version: "v1", Kind: "Namespace", Name: "foo"},
	} {
		t.Run(target.Kind, func(t *testing.T) {
			comp := inventoryTestComposition()
			history, err := makeInventory(comp, []apiv1.ResourceSlice{{
				Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{inventoryTestManifest(t, target)}},
			}})
			require.NoError(t, err)
			resources, err := loadInventoryForTest(history)
			require.NoError(t, err)

			patch := apiv1.Manifest{Manifest: inventoryTestJSON(t, map[string]any{
				"apiVersion": "eno.azure.io/v1", "kind": "Patch",
				"metadata": map[string]any{"name": target.Name, "namespace": target.Namespace},
				"patch": map[string]any{
					"apiVersion": schema.GroupVersion{Group: target.Group, Version: target.Version}.String(),
					"kind":       target.Kind, "ops": []any{},
				},
			})}
			current := []apiv1.ResourceSlice{{Spec: apiv1.ResourceSliceSpec{Resources: []apiv1.Manifest{patch}}}}
			tombstones, err := missingTombstones(current, resources)
			require.NoError(t, err)
			assert.Empty(t, tombstones)

			inventory, err := makeInventory(comp, current)
			require.NoError(t, err)
			recorded, err := loadInventoryForTest(inventory)
			require.NoError(t, err)
			assert.Empty(t, recorded, "a Patch must not establish inventory ownership")

			for _, field := range []string{"group", "kind", "namespace", "name"} {
				other := target
				switch field {
				case "group":
					other.Group = "other.example.com"
				case "kind":
					other.Kind = "Other"
				case "namespace":
					other.Namespace = "other"
				case "name":
					other.Name = "other"
				}
				tombstones, err := missingTombstones(current, []inventoryResource{other})
				require.NoError(t, err)
				require.Len(t, tombstones, 1, "different %s must not match the Patch target", field)
				assert.True(t, tombstones[0].Deleted)
				_, recovered, err := parseInventoryManifest(tombstones[0].Manifest)
				require.NoError(t, err)
				assert.Equal(t, other, recovered)
			}
		})
	}
}

func TestTombstoneRecoveryInventoryMissingTombstonesPreservesEntries(t *testing.T) {
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

func TestTombstoneRecoveryInventoryMissingTombstonesErrors(t *testing.T) {
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

func loadInventoryForTest(items []corev1.Secret) ([]inventoryResource, error) {
	resources, _, err := loadInventory(items)
	return resources, err
}
