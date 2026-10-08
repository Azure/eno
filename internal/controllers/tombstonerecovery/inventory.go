package tombstonerecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	inventoryNamespace      = "kube-system"
	inventoryLineageLabel   = "eno.azure.io/inventory-lineage"
	inventoryDataKey        = "inventory.json"
	inventoryFormatVersion  = "1"
	inventoryMaxDataBytes   = 1 << 20
	inventoryReadinessGroup = "eno.azure.io/readiness-group"
	inventoryDeletionGroup  = "eno.azure.io/deletion-group"

	inventoryFormatVersionAnnotation        = "eno.azure.io/inventory-format-version"
	inventoryCompositionNameAnnotation      = "eno.azure.io/inventory-composition-name"
	inventoryCompositionNamespaceAnnotation = "eno.azure.io/inventory-composition-namespace"
	inventorySynthesizerNameAnnotation      = "eno.azure.io/inventory-synthesizer-name"
	inventorySynthesisUUIDAnnotation        = "eno.azure.io/inventory-synthesis-uuid"
	inventorySynthesizedAnnotation          = "eno.azure.io/inventory-synthesized"
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

func inventoryName(compositionName, lineage string, chunkIndex int) string {
	suffix := "-" + lineage + "-" + strconv.Itoa(chunkIndex)
	prefix := compositionName
	if maxPrefix := validation.DNS1123SubdomainMaxLength - len(suffix); len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], ".-")
	}
	return prefix + suffix
}

func selectInventory(items []corev1.Secret) ([]corev1.Secret, error) {
	var latestTime time.Time
	var latestUUID string
	for i := range items {
		item := &items[i]
		synthesized, err := inventorySynthesized(item)
		if err != nil {
			return nil, &invalidInventoryError{fmt.Errorf("Secret %s/%s: %w", item.Namespace, item.Name, err)}
		}
		if item.Annotations[inventorySynthesisUUIDAnnotation] == "" {
			return nil, &invalidInventoryError{fmt.Errorf("Secret %s/%s has no source synthesis UUID", item.Namespace, item.Name)}
		}
		if latestUUID == "" || synthesized.After(latestTime) {
			latestTime = synthesized
			latestUUID = item.Annotations[inventorySynthesisUUIDAnnotation]
		}
	}
	var selected []corev1.Secret
	for i := range items {
		item := &items[i]
		synthesized, err := inventorySynthesized(item)
		if err != nil {
			return nil, &invalidInventoryError{err}
		}
		uuid := item.Annotations[inventorySynthesisUUIDAnnotation]
		if synthesized.Equal(latestTime) && uuid != latestUUID {
			return nil, &invalidInventoryError{fmt.Errorf("different synthesis UUIDs share the latest inventory timestamp %s", latestTime.Format(time.RFC3339))}
		}
		if uuid == latestUUID {
			// Keep all chunks for this UUID so inconsistent timestamps cannot hide a partial snapshot.
			selected = append(selected, *item)
		}
	}
	return selected, nil
}

func decodeInventorySnapshot(comp *apiv1.Composition, items []corev1.Secret) (resources []inventoryResource, err error) {
	defer func() {
		if err != nil {
			err = &invalidInventoryError{err}
		}
	}()
	if len(items) == 0 {
		return nil, fmt.Errorf("inventory snapshot has no chunks")
	}
	_, count, err := inventoryChunkPosition(&items[0])
	if err != nil {
		return nil, err
	}
	if len(items) != count {
		return nil, fmt.Errorf("inventory snapshot has %d chunks, expected %d", len(items), count)
	}
	chunks := make([][]inventoryResource, count)
	seen := map[resource.Ref]struct{}{}
	for i := range items {
		item := &items[i]
		if err := validateInventoryIdentity(comp, item); err != nil {
			return nil, fmt.Errorf("Secret %s/%s: %w", item.Namespace, item.Name, err)
		}
		index, _, err := inventoryChunkPosition(item)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{
			inventoryCompositionNameAnnotation, inventorySynthesisUUIDAnnotation,
			inventorySynthesizedAnnotation, inventoryChunkCountAnnotation,
		} {
			if item.Annotations[key] != items[0].Annotations[key] {
				return nil, fmt.Errorf("Secret %q has inconsistent %s", item.Name, key)
			}
		}
		if chunks[index] != nil {
			return nil, fmt.Errorf("duplicate inventory chunk index %d", index)
		}
		payload, ok := item.Data[inventoryDataKey]
		if !ok {
			return nil, fmt.Errorf("Secret %q is missing data key %q", item.Name, inventoryDataKey)
		}
		size := 0
		for _, value := range item.Data {
			size += len(value)
		}
		if size > inventoryMaxDataBytes {
			return nil, fmt.Errorf("Secret %q exceeds the %d-byte data limit", item.Name, inventoryMaxDataBytes)
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
		chunks[index] = data
	}
	resources = []inventoryResource{}
	for index, chunk := range chunks {
		if chunk == nil {
			return nil, fmt.Errorf("missing inventory chunk index %d", index)
		}
		resources = append(resources, chunk...)
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

func validateInventoryIdentity(comp *apiv1.Composition, item *corev1.Secret) error {
	if item.Namespace != inventoryNamespace {
		return fmt.Errorf("namespace %q must be %q", item.Namespace, inventoryNamespace)
	}
	if item.DeletionTimestamp != nil {
		return fmt.Errorf("secret is being deleted")
	}
	if item.Annotations[inventoryFormatVersionAnnotation] != inventoryFormatVersion {
		return fmt.Errorf("unsupported inventory format version %q", item.Annotations[inventoryFormatVersionAnnotation])
	}
	if item.Annotations[inventorySynthesisUUIDAnnotation] == "" {
		return fmt.Errorf("missing source synthesis UUID")
	}
	if _, err := inventorySynthesized(item); err != nil {
		return err
	}
	namespace := item.Annotations[inventoryCompositionNamespaceAnnotation]
	synthesizer := item.Annotations[inventorySynthesizerNameAnnotation]
	if namespace != comp.Namespace || synthesizer != comp.Spec.Synthesizer.Name {
		return fmt.Errorf("inventory lineage %q/%q does not match composition lineage %q/%q", namespace, synthesizer, comp.Namespace, comp.Spec.Synthesizer.Name)
	}
	lineage := inventoryLineage(comp)
	if item.Labels[inventoryLineageLabel] != lineage || item.Annotations[inventoryLineageLabel] != lineage {
		return fmt.Errorf("inventory lineage annotation and label must match %q", lineage)
	}
	sourceName := item.Annotations[inventoryCompositionNameAnnotation]
	if problems := validation.IsDNS1123Subdomain(sourceName); len(problems) > 0 {
		return fmt.Errorf("invalid source composition name %q: %s", sourceName, strings.Join(problems, ", "))
	}
	index, _, err := inventoryChunkPosition(item)
	if err != nil {
		return err
	}
	// Validate the stored source name, not the current Composition name: lineage survives recreation.
	if name := inventoryName(sourceName, lineage, index); item.Name != name {
		return fmt.Errorf("name %q must be %q", item.Name, name)
	}
	return nil
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
				Name:      inventoryName(comp.Name, lineage, index),
				Namespace: inventoryNamespace,
				Labels:    map[string]string{inventoryLineageLabel: lineage},
				Annotations: map[string]string{
					inventoryLineageLabel:                   lineage,
					inventoryFormatVersionAnnotation:        inventoryFormatVersion,
					inventoryCompositionNameAnnotation:      comp.Name,
					inventoryCompositionNamespaceAnnotation: comp.Namespace,
					inventorySynthesizerNameAnnotation:      comp.Spec.Synthesizer.Name,
					inventorySynthesisUUIDAnnotation:        syn.UUID,
					inventorySynthesizedAnnotation:          syn.Synthesized.UTC().Format(time.RFC3339),
					inventoryChunkIndexAnnotation:           strconv.Itoa(index),
					inventoryChunkCountAnnotation:           strconv.Itoa(len(payloads)),
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
