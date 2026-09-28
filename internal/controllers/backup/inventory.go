package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	inventoryCompositionNamespaceAnnotation = "eno.azure.io/inventory-composition-namespace"
	inventorySynthesizerNameAnnotation      = "eno.azure.io/inventory-synthesizer-name"
	inventorySynthesisUUIDAnnotation        = "eno.azure.io/inventory-synthesis-uuid"
	inventorySynthesizedAnnotation          = "eno.azure.io/inventory-synthesized"
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

func inventoryName(comp *apiv1.Composition, synthesisUUID string) string {
	return "eno-inventory-" + inventoryLineage(comp) + "-" + synthesisUUID
}

func selectInventory(items []corev1.ConfigMap) (*corev1.ConfigMap, error) {
	var latest *corev1.ConfigMap
	var latestTime time.Time
	for i := range items {
		item := &items[i]
		synthesized, err := inventorySynthesized(item)
		if err != nil {
			return nil, &invalidInventoryError{fmt.Errorf("ConfigMap %s/%s: %w", item.Namespace, item.Name, err)}
		}
		// Equal timestamps keep the first inventory.
		if latest == nil || synthesized.After(latestTime) {
			latest = item
			latestTime = synthesized
		}
	}
	return latest.DeepCopy(), nil
}

func inventoriesMatch(comp *apiv1.Composition, existing, intended *corev1.ConfigMap) (bool, error) {
	if existing == nil || intended == nil {
		return false, nil
	}
	existingResources, err := decodeInventorySnapshot(comp, *existing)
	if err != nil {
		return false, err
	}
	intendedResources, err := decodeInventorySnapshot(comp, *intended)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(existingResources, intendedResources) {
		return false, nil
	}
	for _, key := range []string{
		inventoryFormatVersionAnnotation,
		inventoryCompositionNamespaceAnnotation,
		inventorySynthesizerNameAnnotation,
		inventorySynthesisUUIDAnnotation,
	} {
		if existing.Annotations[key] != intended.Annotations[key] {
			return false, nil
		}
	}
	existingTime, err := inventorySynthesized(existing)
	if err != nil {
		return false, err
	}
	intendedTime, err := inventorySynthesized(intended)
	if err != nil {
		return false, err
	}
	return existingTime.Equal(intendedTime), nil
}

func decodeInventorySnapshot(comp *apiv1.Composition, item corev1.ConfigMap) (resources []inventoryResource, err error) {
	defer func() {
		if err != nil {
			err = &invalidInventoryError{fmt.Errorf("ConfigMap %s/%s: %w", item.Namespace, item.Name, err)}
		}
	}()
	if item.Namespace != inventoryNamespace {
		return nil, fmt.Errorf("namespace %q must be %q", item.Namespace, inventoryNamespace)
	}
	if item.DeletionTimestamp != nil {
		return nil, fmt.Errorf("configmap is being deleted")
	}
	payload, ok := item.Data[inventoryDataKey]
	if !ok {
		return nil, fmt.Errorf("missing data key %q", inventoryDataKey)
	}
	var data []inventoryResource
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", inventoryDataKey, err)
	}
	if err := validateInventoryIdentity(comp, &item); err != nil {
		return nil, err
	}
	return data, nil
}

func inventorySynthesized(item *corev1.ConfigMap) (time.Time, error) {
	synthesized, err := time.Parse(time.RFC3339, item.Annotations[inventorySynthesizedAnnotation])
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid source synthesized timestamp: %w", err)
	}
	if synthesized.IsZero() {
		return time.Time{}, fmt.Errorf("missing or zero source synthesized timestamp")
	}
	return synthesized.UTC(), nil
}

func validateInventoryIdentity(comp *apiv1.Composition, item *corev1.ConfigMap) error {
	namespace := item.Annotations[inventoryCompositionNamespaceAnnotation]
	synthesizer := item.Annotations[inventorySynthesizerNameAnnotation]
	if namespace != comp.Namespace || synthesizer != comp.Spec.Synthesizer.Name {
		return fmt.Errorf("inventory lineage %q/%q does not match composition lineage %q/%q", namespace, synthesizer, comp.Namespace, comp.Spec.Synthesizer.Name)
	}
	if item.Labels[inventoryLineageLabel] != inventoryLineage(comp) {
		return fmt.Errorf("label %s=%q does not match lineage %q", inventoryLineageLabel, item.Labels[inventoryLineageLabel], inventoryLineage(comp))
	}
	if name := inventoryName(comp, item.Annotations[inventorySynthesisUUIDAnnotation]); item.Name != name {
		return fmt.Errorf("name %q must be %q", item.Name, name)
	}
	return nil
}

func makeInventory(comp *apiv1.Composition, slices []apiv1.ResourceSlice) (*corev1.ConfigMap, error) {
	syn := comp.Status.CurrentSynthesis
	data := []inventoryResource{}
	seen := map[resource.Ref]struct{}{}
	for _, slice := range slices {
		for i, manifest := range slice.Spec.Resources {
			if manifest.Deleted {
				continue
			}
			_, res, err := parseInventoryManifest(manifest.Manifest)
			if err != nil {
				return nil, fmt.Errorf("parsing resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
			}
			if res.isPatch() {
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
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encoding inventory: %w", err)
	}
	if len(payload) > inventoryMaxDataBytes {
		return nil, fmt.Errorf("inventory data is %d bytes, exceeding the %d-byte ConfigMap limit", len(payload), inventoryMaxDataBytes)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inventoryName(comp, syn.UUID),
			Namespace: inventoryNamespace,
			Labels:    map[string]string{inventoryLineageLabel: inventoryLineage(comp)},
			Annotations: map[string]string{
				inventoryFormatVersionAnnotation:        inventoryFormatVersion,
				inventoryCompositionNamespaceAnnotation: comp.Namespace,
				inventorySynthesizerNameAnnotation:      comp.Spec.Synthesizer.Name,
				inventorySynthesisUUIDAnnotation:        syn.UUID,
				inventorySynthesizedAnnotation:          syn.Synthesized.UTC().Format(time.RFC3339),
			},
		},
		Data: map[string]string{inventoryDataKey: string(payload)},
	}, nil
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
	res := inventoryResource{
		Group: gvk.Group, Version: gvk.Version, Kind: gvk.Kind,
		Name: obj.GetName(), Namespace: obj.GetNamespace(), Labels: obj.GetLabels(),
	}
	annotations := obj.GetAnnotations()
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
