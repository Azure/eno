package tombstonerecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	// Secret storage and lookup.
	inventoryNamespace          = "kube-system"
	inventoryLineageLabel       = "eno.azure.io/inventory-lineage"
	inventorySynthesisUUIDLabel = "eno.azure.io/inventory-synthesis-uuid"
	inventoryDataKey            = "inventory.json"
	inventoryMaxDataBytes       = 1 << 20

	// Resource metadata retained in each inventory entry.
	inventoryReadinessGroup = "eno.azure.io/readiness-group"
	inventoryDeletionGroup  = "eno.azure.io/deletion-group"

	// Snapshot source metadata written on every Secret chunk.
	inventorySynthesizedAnnotation = "eno.azure.io/inventory-synthesized"

	// Chunk ordering and completeness metadata.
	inventoryChunkIndexAnnotation           = "eno.azure.io/inventory-chunk-index"
	inventoryChunkCountAnnotation           = "eno.azure.io/inventory-chunk-count"
)

type inventoryResource struct {
	Group       string            `json:"group"`
	Version     string            `json:"version"`
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type invalidInventoryError struct {
	err error
}

type synthesisInventory struct {
	synthesisUUID string
	synthesized   time.Time
	secrets       []corev1.Secret
	complete      bool
}

func (e *invalidInventoryError) Error() string {
	return "invalid inventory: " + e.err.Error()
}

func (e *invalidInventoryError) Unwrap() error {
	return e.err
}

func inventoryLineage(comp *apiv1.Composition) string {
	hash := sha256.Sum256([]byte(comp.Namespace + "/" + comp.Spec.Synthesizer.Name))
	return hex.EncodeToString(hash[:16])
}

func inventoryName(lineage, synthesisUUID string, chunkIndex int) string {
	return "eno-inventory-" + lineage + "-" + synthesisUUID + "-" + strconv.Itoa(chunkIndex)
}

func selectInventory(items []corev1.Secret) ([]corev1.Secret, error) {
	if len(items) == 0 {
		return nil, nil
	}
	inventories, err := groupInventorySecretsBySynthesis(items)
	if err != nil {
		return nil, &invalidInventoryError{err}
	}
	if len(inventories) == 1 {
		if !inventories[0].complete {
			return nil, &invalidInventoryError{fmt.Errorf("no complete inventory snapshot found")}
		}
		return inventories[0].secrets, nil
	}
	return selectNewestCompleteInventory(inventories)
}

// groupInventorySecretsBySynthesis builds one inventory per source synthesis UUID.
func groupInventorySecretsBySynthesis(items []corev1.Secret) ([]synthesisInventory, error) {
	byUUID := map[string][]corev1.Secret{}
	for i := range items {
		item := &items[i]
		uuid := item.Labels[inventorySynthesisUUIDLabel]
		if uuid == "" {
			return nil, fmt.Errorf("Secret %s/%s has no source synthesis UUID label", item.Namespace, item.Name)
		}
		byUUID[uuid] = append(byUUID[uuid], *item)
	}
	inventories := make([]synthesisInventory, 0, len(byUUID))
	for uuid, inventorySecrets := range byUUID {
		synthesized, err := inventoryGroupSynthesized(inventorySecrets)
		if err != nil {
			return nil, err
		}
		orderedSecrets, complete := orderInventoryChunks(inventorySecrets)
		inventories = append(inventories, synthesisInventory{
			synthesisUUID: uuid, synthesized: synthesized, secrets: orderedSecrets, complete: complete,
		})
	}
	return inventories, nil
}

// selectNewestCompleteInventory ignores partial inventories and returns the latest usable inventory.
func selectNewestCompleteInventory(inventories []synthesisInventory) ([]corev1.Secret, error) {
	var latestTime time.Time
	incomplete := false
	for i := range inventories {
		inventory := &inventories[i]
		if !inventory.complete {
			incomplete = true
			continue
		}
		if latestTime.IsZero() || inventory.synthesized.After(latestTime) {
			latestTime = inventory.synthesized
		}
	}
	var selected *synthesisInventory
	for i := range inventories {
		inventory := &inventories[i]
		if !inventory.complete || !inventory.synthesized.Equal(latestTime) {
			continue
		}
		if selected != nil {
			return nil, &invalidInventoryError{fmt.Errorf("different synthesis UUIDs share the latest inventory timestamp %s", latestTime.Format(time.RFC3339))}
		}
		selected = inventory
	}
	if selected != nil {
		return selected.secrets, nil
	}
	if incomplete {
		return nil, &invalidInventoryError{fmt.Errorf("no complete inventory snapshot found")}
	}
	return nil, nil
}

func inventoryGroupSynthesized(items []corev1.Secret) (time.Time, error) {
	var synthesized time.Time
	for i := range items {
		current, err := inventorySynthesized(&items[i])
		if err != nil {
			return time.Time{}, fmt.Errorf("Secret %s/%s: %w", items[i].Namespace, items[i].Name, err)
		}
		if synthesized.IsZero() {
			synthesized = current
		} else if !synthesized.Equal(current) {
			return time.Time{}, fmt.Errorf("synthesis UUID %q has inconsistent source timestamps", items[i].Labels[inventorySynthesisUUIDLabel])
		}
	}
	return synthesized, nil
}

// orderInventoryChunks returns chunks in index order and reports whether the set is complete.
func orderInventoryChunks(items []corev1.Secret) ([]corev1.Secret, bool) {
	if len(items) == 0 {
		return nil, false
	}
	_, count, err := inventoryChunkPosition(&items[0])
	if err != nil || len(items) != count {
		return items, false
	}
	ordered := make([]corev1.Secret, count)
	seen := make([]bool, count)
	for i := range items {
		index, currentCount, err := inventoryChunkPosition(&items[i])
		if err != nil || currentCount != count || seen[index] {
			return items, false
		}
		ordered[index] = items[i]
		seen[index] = true
	}
	return ordered, true
}

func decodeInventorySnapshot(items []corev1.Secret) (resources []inventoryResource, err error) {
	defer func() {
		if err != nil {
			err = &invalidInventoryError{err}
		}
	}()
	resources, err = validateInventory(items)
	if err != nil {
		return nil, err
	}
	return resources, nil
}

// validateInventory checks that all chunks form one complete, internally consistent snapshot.
func validateInventory(items []corev1.Secret) ([]inventoryResource, error) {
	ordered, complete := orderInventoryChunks(items)
	if !complete {
		return nil, fmt.Errorf("inventory snapshot chunks are incomplete or invalid")
	}
	if _, err := inventoryGroupSynthesized(ordered); err != nil {
		return nil, err
	}
	seen := map[resource.Ref]struct{}{}
	resources := []inventoryResource{}
	for i := range ordered {
		item := &ordered[i]
		payload, ok := item.Data[inventoryDataKey]
		if !ok {
			return nil, fmt.Errorf("Secret %q is missing data key %q", item.Name, inventoryDataKey)
		}
		var data []inventoryResource
		if err := json.Unmarshal(payload, &data); err != nil {
			return nil, fmt.Errorf("decoding Secret %q: %w", item.Name, err)
		}
		if data == nil {
			return nil, fmt.Errorf("Secret %q inventory must be a JSON array", item.Name)
		}
		for _, res := range data {
			if err := res.validate(); err != nil {
				return nil, fmt.Errorf("Secret %q contains an invalid resource: %w", item.Name, err)
			}
			if res.isPatch() {
				return nil, fmt.Errorf("Secret %q contains a Patch pseudo-resource", item.Name)
			}
			if _, exists := seen[res.ref()]; exists {
				return nil, fmt.Errorf("duplicate inventory identity %v", res.ref())
			}
			seen[res.ref()] = struct{}{}
		}
		resources = append(resources, data...)
	}
	return resources, nil
}

func inventoryChunkPosition(item *corev1.Secret) (int, int, error) {
	index, err := strconv.Atoi(item.Annotations[inventoryChunkIndexAnnotation])
	if err != nil || index < 0 {
		return 0, 0, fmt.Errorf("Secret %q has an invalid chunk index", item.Name)
	}
	count, err := strconv.Atoi(item.Annotations[inventoryChunkCountAnnotation])
	if err != nil || count <= index {
		return 0, 0, fmt.Errorf("Secret %q has an invalid chunk count", item.Name)
	}
	return index, count, nil
}

func inventorySynthesized(item *corev1.Secret) (time.Time, error) {
	synthesized, err := time.Parse(time.RFC3339, item.Annotations[inventorySynthesizedAnnotation])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid source synthesized timestamp: %w", err)
	}
	if synthesized.IsZero() {
		return time.Time{}, fmt.Errorf("missing or zero source synthesized timestamp")
	}
	return synthesized.UTC(), nil
}

func makeInventory(comp *apiv1.Composition, slices []apiv1.ResourceSlice) ([]corev1.Secret, error) {
	syn := comp.Status.CurrentSynthesis
	if syn == nil || syn.UUID == "" || syn.Synthesized == nil || syn.Synthesized.IsZero() {
		return nil, fmt.Errorf("inventory requires a synthesized current synthesis with a UUID")
	}
	data := []inventoryResource{}
	seen := map[resource.Ref]struct{}{}
	for _, slice := range slices {
		for i, manifest := range slice.Spec.Resources {
			_, res, err := parseInventoryManifest(manifest.Manifest)
			if err != nil {
				return nil, fmt.Errorf("parsing resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
			}
			if manifest.Deleted || res.isPatch() {
				continue
			}
			ref := res.ref()
			// Keep the first occurrence's version and metadata; inventory does not need to reproduce the apply winner.
			if _, ok := seen[ref]; ok {
				continue
			}
			seen[ref] = struct{}{}
			data = append(data, res)
		}
	}
	normalizeInventoryResources(data)
	payloads, err := packInventory(data)
	if err != nil {
		return nil, err
	}
	secrets := make([]corev1.Secret, 0, len(payloads))
	lineage := inventoryLineage(comp)
	for index, payload := range payloads {
		secrets = append(secrets, corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      inventoryName(lineage, syn.UUID, index),
				Namespace: inventoryNamespace,
				Labels: map[string]string{
					inventoryLineageLabel:       lineage,
					inventorySynthesisUUIDLabel: syn.UUID,
				},
				Annotations: map[string]string{
					inventorySynthesizedAnnotation: syn.Synthesized.UTC().Format(time.RFC3339),
					inventoryChunkIndexAnnotation:  strconv.Itoa(index),
					inventoryChunkCountAnnotation:  strconv.Itoa(len(payloads)),
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{inventoryDataKey: payload},
		})
	}
	return secrets, nil
}

func packInventory(resources []inventoryResource) ([][]byte, error) {
	var chunks [][]byte
	chunk := []byte{'['}
	for _, res := range resources {
		entry, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("encoding inventory resource: %w", err)
		}
		if len(entry)+2 > inventoryMaxDataBytes {
			return nil, fmt.Errorf("inventory resource %v exceeds the %d-byte Secret data limit", res.ref(), inventoryMaxDataBytes)
		}
		separator := 0
		if len(chunk) > 1 {
			separator = 1
		}
		if len(chunk)+separator+len(entry)+1 > inventoryMaxDataBytes {
			chunks = append(chunks, append(chunk, ']'))
			chunk = []byte{'['}
		}
		if len(chunk) > 1 {
			chunk = append(chunk, ',')
		}
		chunk = append(chunk, entry...)
	}
	return append(chunks, append(chunk, ']')), nil
}

func missingTombstones(slices []apiv1.ResourceSlice, resources []inventoryResource) ([]apiv1.Manifest, error) {
	if resources == nil {
		return nil, nil
	}
	existing := map[resource.Ref]struct{}{}
	for _, slice := range slices {
		for i, manifest := range slice.Spec.Resources {
			obj, _, err := parseInventoryManifest(manifest.Manifest)
			if err != nil {
				return nil, fmt.Errorf("parsing resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
			}
			// Use synthesis semantics: resources, tombstones, and Patch targets protect their identities.
			existing[resource.RefFromUnstructured(obj)] = struct{}{}
		}
	}
	var tombstones []apiv1.Manifest
	for _, res := range resources {
		ref := res.ref()
		if _, ok := existing[ref]; ok {
			continue
		}
		obj := res.object()
		payload, err := obj.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("encoding recovery tombstone %s: %w", &ref, err)
		}
		tombstones = append(tombstones, apiv1.Manifest{Manifest: string(payload), Deleted: true})
		existing[ref] = struct{}{}
	}
	return tombstones, nil
}

func parseInventoryManifest(manifest string) (*unstructured.Unstructured, inventoryResource, error) {
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON([]byte(manifest)); err != nil {
		return nil, inventoryResource{}, fmt.Errorf("invalid manifest JSON: %w", err)
	}
	gvk := obj.GroupVersionKind()
	labels, _, err := unstructured.NestedNullCoercingStringMap(obj.Object, "metadata", "labels")
	if err != nil {
		return nil, inventoryResource{}, fmt.Errorf("invalid manifest labels: %w", err)
	}
	annotations, _, err := unstructured.NestedNullCoercingStringMap(obj.Object, "metadata", "annotations")
	if err != nil {
		return nil, inventoryResource{}, fmt.Errorf("invalid manifest annotations: %w", err)
	}
	res := inventoryResource{
		Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind,
		Name: obj.GetName(), Namespace: obj.GetNamespace(), Labels: labels,
	}
	if err := res.validate(); err != nil {
		return nil, inventoryResource{}, err
	}
	for _, key := range []string{inventoryReadinessGroup, inventoryDeletionGroup} {
		if value, ok := annotations[key]; ok {
			if res.Annotations == nil {
				res.Annotations = map[string]string{}
			}
			res.Annotations[key] = value
		}
	}
	return obj, res, nil
}

func (res inventoryResource) validate() error {
	if res.Version == "" || res.Kind == "" || res.Name == "" {
		return fmt.Errorf("resource identity requires version, kind, and name")
	}
	return nil
}

func (res inventoryResource) ref() resource.Ref {
	return resource.Ref{Group: res.Group, Kind: res.Kind, Namespace: res.Namespace, Name: res.Name}
}

func (res inventoryResource) isPatch() bool {
	return res.Group == "eno.azure.io" && res.Version == "v1" && res.Kind == "Patch"
}

func (res inventoryResource) object() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	obj.SetGroupVersionKind(schema.GroupVersionKind{Group: res.Group, Version: res.Version, Kind: res.Kind})
	obj.SetName(res.Name)
	if res.Namespace != "" {
		obj.SetNamespace(res.Namespace)
	}
	if len(res.Labels) != 0 {
		obj.SetLabels(maps.Clone(res.Labels))
	}
	if len(res.Annotations) != 0 {
		obj.SetAnnotations(maps.Clone(res.Annotations))
	}
	return obj
}

func normalizeInventoryResources(resources []inventoryResource) {
	for i := range resources {
		if len(resources[i].Labels) == 0 {
			resources[i].Labels = nil
		}
		if len(resources[i].Annotations) == 0 {
			resources[i].Annotations = nil
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		for _, pair := range [][2]string{{a.Group, b.Group}, {a.Kind, b.Kind}, {a.Namespace, b.Namespace}, {a.Name, b.Name}} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return a.Version < b.Version
	})
}
