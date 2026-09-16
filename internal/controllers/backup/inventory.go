package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	enocel "github.com/Azure/eno/internal/cel"
	"github.com/Azure/eno/internal/resource"
	"github.com/google/cel-go/cel"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	kjson "sigs.k8s.io/json"
)

const (
	inventoryNamespace      = "kube-system"
	inventoryLineageLabel   = "eno.azure.io/inventory-lineage"
	inventoryDataKey        = "inventory.json"
	inventoryFormatVersion  = 1
	inventoryMaxDataBytes   = 1 << 20
	inventoryReadinessGroup = "eno.azure.io/readiness-group"
	inventoryDeletionGroup  = "eno.azure.io/deletion-group"
)

type inventorySnapshot struct {
	Object corev1.ConfigMap
	Data   inventory
}

type inventory struct {
	FormatVersion        int                         `json:"formatVersion"`
	CompositionNamespace string                      `json:"compositionNamespace"`
	SynthesizerName      string                      `json:"synthesizerName"`
	SynthesisUUID        string                      `json:"synthesisUUID"`
	Synthesized          metav1.Time                 `json:"synthesized"`
	Resources            []inventoryResource         `json:"resources"`
	SourceComposition    *inventorySourceComposition `json:"sourceComposition,omitempty"`
}

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

func (e *invalidInventoryError) Error() string { return "invalid inventory: " + e.err.Error() }
func (e *invalidInventoryError) Unwrap() error { return e.err }

func inventoryLineage(comp *apiv1.Composition) string {
	hash := sha256.Sum256([]byte(comp.Namespace + "/" + comp.Spec.Synthesizer.Name))
	return hex.EncodeToString(hash[:16])
}

func inventoryName(comp *apiv1.Composition, synthesisUUID string) string {
	return "eno-inventory-" + inventoryLineage(comp) + "-" + synthesisUUID
}

func selectInventory(comp *apiv1.Composition, items []corev1.ConfigMap) (*inventorySnapshot, error) {
	var selection inventorySelection
	for _, item := range items {
		snapshot, err := decodeInventory(comp, item)
		if err != nil {
			return nil, err
		}
		if err := selection.add(snapshot); err != nil {
			return nil, err
		}
	}
	return selection.selected()
}

// Candidates must be decoded before adding them so their payloads are validated
// and normalized. Retain only a hash per UUID and the latest full snapshot.
type inventorySelection struct {
	latest   *inventorySnapshot
	tiedUUID string
	seen     map[string][sha256.Size]byte
	err      error
}

func (selection *inventorySelection) add(snapshot *inventorySnapshot) (err error) {
	defer func() {
		if err != nil {
			selection.err = err
		}
	}()
	if selection.err != nil {
		return selection.err
	}
	if snapshot == nil {
		return &invalidInventoryError{fmt.Errorf("snapshot is nil")}
	}
	data := snapshot.Data
	// metav1.Time.MarshalJSON drops fractional seconds, but decoded timestamps
	// can retain them. Hash their full precision to detect conflicting payloads.
	payload, err := json.Marshal(struct {
		inventory
		Synthesized string `json:"synthesized"`
	}{
		inventory:   data,
		Synthesized: data.Synthesized.Time.Format(time.RFC3339Nano),
	})
	if err != nil {
		return &invalidInventoryError{fmt.Errorf("hashing synthesis %q: %w", data.SynthesisUUID, err)}
	}
	hash := sha256.Sum256(payload)
	if previous, ok := selection.seen[data.SynthesisUUID]; ok && previous != hash {
		return &invalidInventoryError{fmt.Errorf("conflicting snapshots for synthesis %q", data.SynthesisUUID)}
	}
	if selection.seen == nil {
		selection.seen = map[string][sha256.Size]byte{}
	}
	selection.seen[data.SynthesisUUID] = hash
	if selection.latest == nil || data.Synthesized.After(selection.latest.Data.Synthesized.Time) {
		selection.latest = snapshot
		selection.tiedUUID = ""
	} else if data.Synthesized.Equal(&selection.latest.Data.Synthesized) && data.SynthesisUUID != selection.latest.Data.SynthesisUUID {
		selection.tiedUUID = data.SynthesisUUID
	}
	return nil
}

func (selection *inventorySelection) selected() (*inventorySnapshot, error) {
	if selection.err != nil {
		return nil, selection.err
	}
	if selection.tiedUUID != "" {
		return nil, &invalidInventoryError{fmt.Errorf("syntheses %q and %q share greatest source synthesized timestamp %s", selection.latest.Data.SynthesisUUID, selection.tiedUUID, selection.latest.Data.Synthesized.Time)}
	}
	return selection.latest, nil
}

func inventoriesMatch(existing, intended *inventorySnapshot) bool {
	if existing == nil || intended == nil {
		return false
	}
	existingData, intendedData := existing.Data, intended.Data
	// A recording retry may find a legacy snapshot. Accept its unchanged core
	// without fabricating provenance or modifying the persisted ConfigMap.
	if existingData.SourceComposition == nil {
		intendedData.SourceComposition = nil
	}
	return reflect.DeepEqual(existingData, intendedData)
}

func decodeInventory(comp *apiv1.Composition, item corev1.ConfigMap) (*inventorySnapshot, error) {
	if comp == nil {
		return nil, &invalidInventoryError{fmt.Errorf("ConfigMap %s/%s: composition is nil", item.Namespace, item.Name)}
	}
	return decodeInventorySnapshot(comp, item)
}

func decodeInventoryForCleanup(item corev1.ConfigMap) (*inventorySnapshot, error) {
	return decodeInventorySnapshot(nil, item)
}

func decodeInventorySnapshot(comp *apiv1.Composition, item corev1.ConfigMap) (snapshot *inventorySnapshot, err error) {
	defer func() {
		if err != nil {
			err = &invalidInventoryError{fmt.Errorf("ConfigMap %s/%s: %w", item.Namespace, item.Name, err)}
		}
	}()
	if item.Namespace != inventoryNamespace {
		return nil, fmt.Errorf("namespace %q must be %q", item.Namespace, inventoryNamespace)
	}
	if item.DeletionTimestamp != nil {
		return nil, fmt.Errorf("snapshot is being deleted")
	}
	size := 0
	for _, value := range item.Data {
		size += len(value)
	}
	for _, value := range item.BinaryData {
		size += len(value)
	}
	if size > inventoryMaxDataBytes {
		return nil, fmt.Errorf("ConfigMap data is %d bytes, exceeding the %d-byte limit", size, inventoryMaxDataBytes)
	}
	payload, ok := item.Data[inventoryDataKey]
	if !ok {
		return nil, fmt.Errorf("missing data key %q", inventoryDataKey)
	}
	var data inventory
	if err := unmarshalInventoryJSON([]byte(payload), &data); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", inventoryDataKey, err)
	}
	if comp == nil {
		comp = &apiv1.Composition{
			ObjectMeta: metav1.ObjectMeta{Namespace: data.CompositionNamespace},
			Spec:       apiv1.CompositionSpec{Synthesizer: apiv1.SynthesizerRef{Name: data.SynthesizerName}},
		}
	}
	if err := validateInventory(comp, &data); err != nil {
		return nil, err
	}
	if item.Labels[inventoryLineageLabel] != inventoryLineage(comp) {
		return nil, fmt.Errorf("label %s=%q does not match lineage %q", inventoryLineageLabel, item.Labels[inventoryLineageLabel], inventoryLineage(comp))
	}
	if item.Name != inventoryName(comp, data.SynthesisUUID) {
		return nil, fmt.Errorf("name %q must be %q", item.Name, inventoryName(comp, data.SynthesisUUID))
	}

	// Typed JSON decoding accepts null string values as empty strings. Check the
	// wire types too, rather than silently converting malformed identity/labels.
	var wire struct {
		Resources         []map[string]any `json:"resources"`
		SourceComposition map[string]any   `json:"sourceComposition"`
	}
	if err := json.Unmarshal([]byte(payload), &wire); err != nil {
		return nil, fmt.Errorf("decoding inventory field types: %w", err)
	}
	if err := validateInventorySourceWire(wire.SourceComposition); err != nil {
		return nil, fmt.Errorf("sourceComposition: %w", err)
	}
	for i, obj := range wire.Resources {
		for _, key := range []string{"group", "version", "kind", "namespace", "name"} {
			if _, ok := obj[key].(string); !ok {
				return nil, fmt.Errorf("resources[%d].%s must be present and a string", i, key)
			}
		}
		for _, key := range []string{"labels", "annotations"} {
			if _, err := inventoryStringMap(obj, key); err != nil {
				return nil, fmt.Errorf("resources[%d].%s: %w", i, key, err)
			}
		}
	}
	return &inventorySnapshot{Object: *item.DeepCopy(), Data: data}, nil
}

func unmarshalInventoryJSON(data []byte, into any) error {
	strictErrors, err := kjson.UnmarshalStrict(data, into)
	if err != nil {
		return err
	}
	return errors.Join(strictErrors...)
}

func validateInventory(comp *apiv1.Composition, data *inventory) error {
	if data.FormatVersion != inventoryFormatVersion {
		return fmt.Errorf("unsupported formatVersion %d", data.FormatVersion)
	}
	if err := validateInventoryLineage(data); err != nil {
		return err
	}
	if data.CompositionNamespace != comp.Namespace || data.SynthesizerName != comp.Spec.Synthesizer.Name {
		return fmt.Errorf("payload lineage %q/%q does not match composition lineage %q/%q", data.CompositionNamespace, data.SynthesizerName, comp.Namespace, comp.Spec.Synthesizer.Name)
	}
	parsedUUID, err := uuid.Parse(data.SynthesisUUID)
	if err != nil {
		return fmt.Errorf("invalid synthesisUUID %q: %w", data.SynthesisUUID, err)
	}
	if parsedUUID.String() != data.SynthesisUUID {
		return fmt.Errorf("synthesisUUID %q must use canonical lowercase hyphenated UUID encoding", data.SynthesisUUID)
	}
	if data.Synthesized.IsZero() {
		return fmt.Errorf("missing or zero source synthesized timestamp")
	}
	data.Synthesized.Time = data.Synthesized.Time.UTC().Round(0)
	if data.Resources == nil {
		return fmt.Errorf("resources must be a non-null array")
	}
	if err := data.SourceComposition.validate(); err != nil {
		return fmt.Errorf("sourceComposition: %w", err)
	}
	seen := map[resource.Ref]struct{}{}
	for i := range data.Resources {
		res := &data.Resources[i]
		if err := validateInventoryResource(res); err != nil {
			return fmt.Errorf("resources[%d]: %w", i, err)
		}
		if res.isPatch() {
			return fmt.Errorf("resources[%d]: Eno Patch pseudo-resources cannot be inventoried", i)
		}
		ref := res.ref()
		if _, ok := seen[ref]; ok {
			return fmt.Errorf("resources[%d]: duplicate identity %s", i, &ref)
		}
		seen[ref] = struct{}{}
	}
	sortInventoryResources(data.Resources)
	return nil
}

func validateInventoryLineage(data *inventory) error {
	if errs := validation.IsDNS1123Label(data.CompositionNamespace); len(errs) != 0 {
		return fmt.Errorf("invalid compositionNamespace %q: %s", data.CompositionNamespace, strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(data.SynthesizerName); len(errs) != 0 {
		return fmt.Errorf("invalid synthesizerName %q: %s", data.SynthesizerName, strings.Join(errs, "; "))
	}
	return nil
}

func makeInventory(ctx context.Context, comp *apiv1.Composition, slices []apiv1.ResourceSlice, filter cel.Program) (*inventorySnapshot, error) {
	if comp == nil || comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil {
		return nil, fmt.Errorf("inventory requires a current synthesis with a source synthesized timestamp")
	}
	source, err := makeInventorySource(comp)
	if err != nil {
		return nil, fmt.Errorf("building inventory sourceComposition: %w", err)
	}
	data := inventory{
		FormatVersion:        inventoryFormatVersion,
		CompositionNamespace: comp.Namespace,
		SynthesizerName:      comp.Spec.Synthesizer.Name,
		SynthesisUUID:        comp.Status.CurrentSynthesis.UUID,
		Synthesized:          *comp.Status.CurrentSynthesis.Synthesized,
		Resources:            []inventoryResource{},
		SourceComposition:    source,
	}
	if err := validateInventory(comp, &data); err != nil {
		return nil, fmt.Errorf("building inventory: %w", err)
	}
	type selectedResource struct {
		data       inventoryResource
		definition *resource.Resource
	}
	selected := map[resource.Ref]selectedResource{}
	for _, slice := range slices {
		for i, manifest := range slice.Spec.Resources {
			obj, res, err := parseInventoryManifest(manifest.Manifest)
			if err != nil {
				return nil, fmt.Errorf("parsing resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
			}
			if manifest.Deleted || res.isPatch() {
				continue
			}
			matches, err := inventoryFilterMatches(ctx, comp, obj, filter)
			if err != nil {
				return nil, fmt.Errorf("filtering resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
			}
			if !matches {
				continue
			}
			definition, err := resource.FromSlice(ctx, comp, &slice, i)
			if err != nil {
				return nil, fmt.Errorf("loading resource %d of slice %s/%s for inventory: %w", i, slice.Namespace, slice.Name, err)
			}
			ref := res.ref()
			// Match resource.treeBuilder.Add: filter first, then retain the
			// greatest original manifest hash, replacing on equal hashes.
			if previous, ok := selected[ref]; ok && definition.Less(previous.definition) {
				continue
			}
			selected[ref] = selectedResource{data: res, definition: definition}
		}
	}
	for _, res := range selected {
		data.Resources = append(data.Resources, res.data)
	}
	sortInventoryResources(data.Resources)
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("encoding inventory: %w", err)
	}
	if len(payload) > inventoryMaxDataBytes {
		return nil, fmt.Errorf("inventory data is %d bytes, exceeding the %d-byte ConfigMap limit", len(payload), inventoryMaxDataBytes)
	}
	item := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      inventoryName(comp, data.SynthesisUUID),
			Namespace: inventoryNamespace,
			Labels:    map[string]string{inventoryLineageLabel: inventoryLineage(comp)},
		},
		Data: map[string]string{inventoryDataKey: string(payload)},
	}
	// Round-trip metav1.Time through its wire format so built and decoded data
	// compare identically, including its timestamp precision and time zone.
	snapshot, err := decodeInventory(comp, item)
	if err != nil {
		return nil, fmt.Errorf("validating encoded inventory: %w", err)
	}
	return snapshot, nil
}

func missingTombstones(ctx context.Context, comp *apiv1.Composition, slices []apiv1.ResourceSlice, snapshot *inventorySnapshot, filter cel.Program) ([]apiv1.Manifest, error) {
	if snapshot == nil {
		return nil, nil
	}
	if comp == nil {
		return nil, fmt.Errorf("composition is nil")
	}
	existing := map[resource.Ref]struct{}{}
	for _, slice := range slices {
		for i, manifest := range slice.Spec.Resources {
			obj, res, err := parseInventoryManifest(manifest.Manifest)
			if err != nil {
				return nil, fmt.Errorf("parsing resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
			}
			if res.isPatch() {
				res, err = inventoryPatchTarget(obj, res)
				if err != nil {
					return nil, fmt.Errorf("parsing patch target of resource %d of slice %s/%s: %w", i, slice.Namespace, slice.Name, err)
				}
			}
			// Even a resource excluded by today's filter must not be tombstoned
			// using its historical labels. Existing tombstones also protect it.
			existing[res.ref()] = struct{}{}
		}
	}
	resources := append([]inventoryResource{}, snapshot.Data.Resources...)
	sortInventoryResources(resources)
	var tombstones []apiv1.Manifest
	for _, res := range resources {
		ref := res.ref()
		if _, ok := existing[ref]; ok {
			continue
		}
		obj := res.object()
		matches, err := inventoryFilterMatches(ctx, comp, obj, filter)
		if err != nil {
			return nil, fmt.Errorf("filtering recovery tombstone %s: %w", &ref, err)
		}
		if !matches {
			continue
		}
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
	var res inventoryResource
	if err := unmarshalInventoryJSON([]byte(manifest), &obj.Object); err != nil {
		return nil, res, fmt.Errorf("invalid manifest JSON: %w", err)
	}
	apiVersion, _, err := unstructured.NestedString(obj.Object, "apiVersion")
	if err != nil {
		return nil, res, err
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, res, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
	}
	if apiVersion != gv.String() {
		return nil, res, fmt.Errorf("invalid apiVersion %q: expected %q", apiVersion, gv.String())
	}
	res.Group, res.Version = gv.Group, gv.Version
	if res.Kind, _, err = unstructured.NestedString(obj.Object, "kind"); err != nil {
		return nil, res, err
	}
	if res.Name, _, err = unstructured.NestedString(obj.Object, "metadata", "name"); err != nil {
		return nil, res, err
	}
	if res.Namespace, _, err = unstructured.NestedString(obj.Object, "metadata", "namespace"); err != nil {
		return nil, res, err
	}
	if res.Labels, err = inventoryStringMap(obj.Object, "metadata", "labels"); err != nil {
		return nil, res, err
	}
	annotations, err := inventoryStringMap(obj.Object, "metadata", "annotations")
	if err != nil {
		return nil, res, err
	}
	for _, key := range []string{inventoryReadinessGroup, inventoryDeletionGroup} {
		if value, ok := annotations[key]; ok {
			if res.Annotations == nil {
				res.Annotations = map[string]string{}
			}
			res.Annotations[key] = value
		}
	}
	if err := validateInventoryResource(&res); err != nil {
		return nil, res, err
	}
	if res.isPatch() {
		if _, err := inventoryPatchTarget(obj, res); err != nil {
			return nil, res, fmt.Errorf("invalid patch target: %w", err)
		}
	}
	// Match resource.FromSlice's filter input without parsing readiness checks.
	delete(obj.Object, "status")
	obj.SetCreationTimestamp(metav1.Time{})
	return obj, res, nil
}

func inventoryPatchTarget(obj *unstructured.Unstructured, res inventoryResource) (inventoryResource, error) {
	apiVersion, _, err := unstructured.NestedString(obj.Object, "patch", "apiVersion")
	if err != nil {
		return res, err
	}
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return res, fmt.Errorf("invalid patch apiVersion %q: %w", apiVersion, err)
	}
	if apiVersion != gv.String() {
		return res, fmt.Errorf("invalid patch apiVersion %q: expected %q", apiVersion, gv.String())
	}
	res.Group, res.Version = gv.Group, gv.Version
	if res.Kind, _, err = unstructured.NestedString(obj.Object, "patch", "kind"); err != nil {
		return res, err
	}
	return res, validateInventoryResource(&res)
}

func inventoryStringMap(obj map[string]any, fields ...string) (map[string]string, error) {
	value, _, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	result, _, err := unstructured.NestedStringMap(obj, fields...)
	return result, err
}

func validateInventoryResource(res *inventoryResource) error {
	if res.Group != "" {
		if errs := validation.IsDNS1123Subdomain(res.Group); len(errs) != 0 {
			return fmt.Errorf("invalid group %q: %s", res.Group, strings.Join(errs, "; "))
		}
	}
	if errs := validation.IsDNS1035Label(res.Version); len(errs) != 0 {
		return fmt.Errorf("invalid version %q: %s", res.Version, strings.Join(errs, "; "))
	}
	if errs := validation.IsCIdentifier(res.Kind); len(errs) != 0 {
		return fmt.Errorf("invalid kind %q: %s", res.Kind, strings.Join(errs, "; "))
	}
	if res.Name == "" {
		return fmt.Errorf("resource name is required")
	}
	// Kind-specific DNS name and namespace-scope rules require discovery.
	// Path validation also permits valid non-DNS names such as RBAC role names.
	if errs := pathvalidation.IsValidPathSegmentName(res.Name); len(errs) != 0 {
		return fmt.Errorf("invalid name %q: %s", res.Name, strings.Join(errs, "; "))
	}
	if res.Namespace != "" {
		if errs := validation.IsDNS1123Label(res.Namespace); len(errs) != 0 {
			return fmt.Errorf("invalid namespace %q: %s", res.Namespace, strings.Join(errs, "; "))
		}
	}
	if errs := metavalidation.ValidateLabels(res.Labels, field.NewPath("labels")); len(errs) != 0 {
		return errs.ToAggregate()
	}
	if errs := apivalidation.ValidateAnnotations(res.Annotations, field.NewPath("annotations")); len(errs) != 0 {
		return errs.ToAggregate()
	}
	for key := range res.Annotations {
		if key != inventoryReadinessGroup && key != inventoryDeletionGroup {
			return fmt.Errorf("unsupported inventory annotation %q", key)
		}
	}
	if len(res.Labels) == 0 {
		res.Labels = nil
	}
	if len(res.Annotations) == 0 {
		res.Annotations = nil
	}
	return nil
}

func inventoryFilterMatches(ctx context.Context, comp *apiv1.Composition, obj *unstructured.Unstructured, filter cel.Program) (bool, error) {
	if filter == nil {
		return true, nil
	}
	result, err := enocel.Eval(ctx, filter, comp, obj, nil)
	if err != nil {
		return false, err
	}
	matches, ok := result.Value().(bool)
	if !ok {
		return false, fmt.Errorf("resource filter expression must return a boolean, got %T", result.Value())
	}
	return matches, nil
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

func sortInventoryResources(resources []inventoryResource) {
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
