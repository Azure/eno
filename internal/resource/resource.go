package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	"github.com/Azure/eno/internal/resource/mutation"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

var patchGVK = schema.GroupVersionKind{
	Group:   "eno.azure.io",
	Version: "v1",
	Kind:    "Patch",
}

// Ref refers to a specific synthesized resource.
type Ref struct {
	Name, Namespace, Group, Kind string
}

func (r *Ref) String() string {
	return fmt.Sprintf("(%s.%s)/%s/%s", r.Group, r.Kind, r.Namespace, r.Name)
}

// ManifestRef references a particular resource manifest within a resource slice.
type ManifestRef struct {
	Slice types.NamespacedName
	Index int // position of this manifest within the slice
}

// Resource is the controller's internal representation of a single resource out of a ResourceSlice.
type Resource struct {
	Ref             Ref
	ManifestRef     ManifestRef
	GVK             schema.GroupVersionKind
	ReadinessChecks readiness.Checks
	Labels          map[string]string

	// DefinedGroupKind is set on CRDs to represent the resource type they define.
	DefinedGroupKind *schema.GroupKind

	parsed           *unstructured.Unstructured
	isPatch          bool
	manifestHash     []byte
	manifestDeleted  bool
	readinessGroup   int
	overrides        []*mutation.Op
	latestKnownState atomic.Pointer[apiv1.ResourceState]
}

func (r *Resource) State() *apiv1.ResourceState { return r.latestKnownState.Load() }

// Less returns true when r < than.
// Used to establish determinstic ordering for conflicting resources.
func (r *Resource) Less(than *Resource) bool {
	return bytes.Compare(r.manifestHash, than.manifestHash) < 0
}

// Snapshot evaluates the resource against its current/actual state and returns the resulting "snapshot".
//
// The snapshot should only be used to progress the resource's state from the given resourceVersion.
// Call this function again to get an updated snapshot with the latest state if applying the current snapshot results in a conflict.
func (r *Resource) Snapshot(ctx context.Context, comp *apiv1.Composition, actual *unstructured.Unstructured) (*Snapshot, error) {
	return r.SnapshotWithOverrides(ctx, comp, actual, r)
}

// SnapshotWithOverrides is identical to Snapshot but applies the overrides from another resource
// (presumably a newer version of the same object).
func (r *Resource) SnapshotWithOverrides(ctx context.Context, comp *apiv1.Composition, actual *unstructured.Unstructured, overrideRes *Resource) (*Snapshot, error) {
	copy := r.parsed.DeepCopy()

	for i, op := range overrideRes.overrides {
		err := op.Apply(ctx, comp, actual, copy)
		if err != nil {
			return nil, fmt.Errorf("applying override %d: %w", i+1, err)
		}
	}

	snap := &Snapshot{
		Resource: r,
		parsed:   copy,
	}

	anno := copy.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}

	const disableUpdatesKey = "eno.azure.io/disable-updates"
	snap.DisableUpdates = anno[disableUpdatesKey] == "true"

	const replaceKey = "eno.azure.io/replace"
	snap.Replace = anno[replaceKey] == "true"

	const reconcileIntervalKey = "eno.azure.io/reconcile-interval"
	if str, ok := anno[reconcileIntervalKey]; ok {
		reconcileInterval, err := time.ParseDuration(str)
		if anno[reconcileIntervalKey] != "" && err != nil {
			logr.FromContextOrDiscard(ctx).V(0).Info("invalid reconcile interval - ignoring")
		}
		snap.ReconcileInterval = &metav1.Duration{Duration: reconcileInterval}
	}

	// Remove any eno.azure.io annotations and labels
	copy.SetAnnotations(pruneMetadata(copy.GetAnnotations()))
	copy.SetLabels(pruneMetadata(copy.GetLabels()))

	return snap, nil
}

// Snapshot is a representation of a resource in the context of its current state.
// Practically speaking this means it's a Resource that has had any overrides applied.
type Snapshot struct {
	*Resource

	ReconcileInterval *metav1.Duration
	DisableUpdates    bool
	Replace           bool

	parsed *unstructured.Unstructured
}

func (r *Snapshot) Unstructured() *unstructured.Unstructured {
	// NOTE(jordan): This probably doesn't need to be deep copied. Leaving it during some refactoring, maybe carefully remove it for perf later.
	return r.parsed.DeepCopy()
}

func (r *Snapshot) Deleted(comp *apiv1.Composition) bool {
	return (comp.DeletionTimestamp != nil && !comp.ShouldOrphanResources()) || r.manifestDeleted || (r.isPatch && r.patchSetsDeletionTimestamp())
}

func (r *Snapshot) Patch() ([]byte, bool, error) {
	if !r.isPatch {
		return nil, false, nil
	}

	ops, _, _ := unstructured.NestedSlice(r.parsed.Object, "patch", "ops")
	if len(ops) == 0 {
		return nil, true, nil // empty patch == empty json
	}
	js, err := json.Marshal(&ops)
	if err != nil {
		return nil, false, err
	}
	return js, true, nil
}

func (r *Snapshot) patchSetsDeletionTimestamp() bool {
	ops, _, _ := unstructured.NestedSlice(r.parsed.Object, "patch", "ops")
	for _, op := range ops {
		op, _ := op.(map[string]any)
		if str, ok := op["value"].(string); !ok || str == "" {
			continue
		}
		if op["path"] == "/metadata/deletionTimestamp" {
			return true
		}
	}
	return false
}

// FromSlice constructs a resource out of the given resource slice.
// Some invalid metadata is tolerated. See FromUnstructured for strict validation.
func FromSlice(ctx context.Context, slice *apiv1.ResourceSlice, index int) (*Resource, error) {
	resource := slice.Spec.Resources[index]

	hash := fnv.New64()
	hash.Write([]byte(resource.Manifest))

	parsed := &unstructured.Unstructured{}
	err := parsed.UnmarshalJSON([]byte(resource.Manifest))
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	// Prune out the status/creation time.
	// This is a pragmatic choice to make Eno behave in expected ways for synthesizers written using client-go structs,
	// which set metadata.creationTime=null and status={}.
	if parsed.Object != nil {
		delete(parsed.Object, "status")
		parsed.SetCreationTimestamp(metav1.Time{})
	}

	res, err := newResource(ctx, parsed, false)
	if err != nil {
		return nil, err
	}

	res.manifestHash = hash.Sum(nil)
	res.manifestDeleted = resource.Deleted
	res.ManifestRef.Slice.Name = slice.Name
	res.ManifestRef.Slice.Namespace = slice.Namespace
	res.ManifestRef.Index = index

	return res, nil
}

// FromUnstructured constructs a new resource with strict validation of any Eno metadata such as annotations.
func FromUnstructured(parsed *unstructured.Unstructured) (*Resource, error) {
	return newResource(context.Background(), parsed, true)
}

func newResource(ctx context.Context, parsed *unstructured.Unstructured, strict bool) (*Resource, error) {
	logger := logr.FromContextOrDiscard(ctx)
	res := &Resource{}
	res.parsed = parsed

	gvk := parsed.GroupVersionKind()
	res.GVK = gvk
	res.Ref.Name = parsed.GetName()
	res.Ref.Namespace = parsed.GetNamespace()
	res.Ref.Group = parsed.GroupVersionKind().Group
	res.Ref.Kind = parsed.GetKind()
	logger = logger.WithValues("resourceKind", parsed.GetKind(), "resourceName", parsed.GetName(), "resourceNamespace", parsed.GetNamespace())

	if res.Ref.Name == "" || res.Ref.Kind == "" || parsed.GetAPIVersion() == "" {
		return nil, fmt.Errorf("missing name, kind, or apiVersion")
	}

	if res.GVK == patchGVK {
		res.isPatch = true

		apiVersion, _, _ := unstructured.NestedString(parsed.Object, "patch", "apiVersion")
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return nil, fmt.Errorf("parsing patch apiVersion: %w", err)
		}
		res.GVK.Group = gv.Group
		res.GVK.Version = gv.Version

		res.GVK.Kind, _, _ = unstructured.NestedString(parsed.Object, "patch", "kind")
	}

	if res.GVK.Group == "apiextensions.k8s.io" && res.GVK.Kind == "CustomResourceDefinition" {
		res.DefinedGroupKind = &schema.GroupKind{}
		res.DefinedGroupKind.Group, _, _ = unstructured.NestedString(parsed.Object, "spec", "group")
		res.DefinedGroupKind.Kind, _, _ = unstructured.NestedString(parsed.Object, "spec", "names", "kind")
	}

	res.Labels = maps.Clone(parsed.GetLabels())
	anno := parsed.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}

	const overridesKey = "eno.azure.io/overrides"
	if js, ok := anno[overridesKey]; ok {
		err := json.Unmarshal([]byte(js), &res.overrides)
		if strict && err != nil {
			return nil, fmt.Errorf("invalid override: %w", err)
		}
		if err != nil {
			logger.Error(err, "invalid override json")
		}
	}

	const readinessGroupKey = "eno.azure.io/readiness-group"
	if str, ok := anno[readinessGroupKey]; ok {
		rg, err := strconv.Atoi(str)
		if strict && err != nil {
			return nil, fmt.Errorf("invalid readiness group value: %q", str)
		}
		if err != nil {
			logger.V(0).Info("invalid readiness group - ignoring")
		} else {
			res.readinessGroup = rg
		}
	}

	for key, value := range anno {
		if !strings.HasPrefix(key, "eno.azure.io/readiness") || key == readinessGroupKey {
			continue
		}

		name := strings.TrimPrefix(key, "eno.azure.io/readiness-")
		if name == "eno.azure.io/readiness" {
			name = "default"
		}

		check, err := readiness.ParseCheck(value)
		if strict && err != nil {
			return nil, fmt.Errorf("invalid readiness expression: %w", err)
		}
		if err != nil {
			logger.Error(err, "invalid cel expression")
			continue
		}
		check.Name = name
		res.ReadinessChecks = append(res.ReadinessChecks, check)
	}
	sort.Slice(res.ReadinessChecks, func(i, j int) bool { return res.ReadinessChecks[i].Name < res.ReadinessChecks[j].Name })

	return res, nil
}

func pruneMetadata(m map[string]string) map[string]string {
	maps.DeleteFunc(m, func(key string, value string) bool {
		return strings.HasPrefix(key, "eno.azure.io/")
	})
	if len(m) == 0 {
		m = nil
	}
	return m
}

func NewInputRevisions(obj client.Object, refKey string) *apiv1.InputRevisions {
	ir := apiv1.InputRevisions{
		Key:             refKey,
		ResourceVersion: obj.GetResourceVersion(),
	}
	if rev, _ := strconv.Atoi(obj.GetAnnotations()["eno.azure.io/revision"]); rev != 0 {
		ir.Revision = &rev
	}
	if rev, _ := strconv.ParseInt(obj.GetAnnotations()["eno.azure.io/synthesizer-generation"], 10, 64); rev != 0 {
		ir.SynthesizerGeneration = &rev
	}
	if rev, _ := strconv.ParseInt(obj.GetAnnotations()["eno.azure.io/composition-generation"], 10, 64); rev != 0 {
		ir.CompositionGeneration = &rev
	}
	return &ir
}

// MergeEnoManagedFields corrects managed fields drift to ensure Eno can remove fields
// that are no longer set by the synthesizer, even when another client corrupts the
// managed fields metadata. Returns corrected managed fields, affected field paths,
// and whether correction was needed.
func MergeEnoManagedFields(prev, current, next []metav1.ManagedFieldsEntry) (copy []metav1.ManagedFieldsEntry, fields string, modified bool) {
	prevEnoSet := parseEnoFields(prev)
	nextEnoSet := parseEnoFields(next)

	if prevEnoSet.Empty() {
		return nil, "", false
	}

	currentEnoSet := parseEnoFields(current)

	var expectedFields *fieldpath.Set
	if !nextEnoSet.Empty() && currentEnoSet.Empty() {
		expectedFields = prevEnoSet
	} else {
		expectedFields = prevEnoSet.Difference(nextEnoSet)
		if expectedFields.Empty() {
			return nil, "", false
		}

		expectedFields = expectedFields.Intersection(parseAllFields(current))
		if expectedFields.Empty() {
			return nil, "", false
		}
	}

	return adjustManagedFields(prev, expectedFields), expectedFields.String(), true
}

func adjustManagedFields(entries []metav1.ManagedFieldsEntry, expected *fieldpath.Set) []metav1.ManagedFieldsEntry {
	copy := make([]metav1.ManagedFieldsEntry, 0, len(entries))

	for _, entry := range entries {
		if entry.FieldsV1 == nil {
			copy = append(copy, entry)
			continue
		}

		set := parseFieldsEntry(entry)
		if set == nil {
			copy = append(copy, entry)
			continue
		}

		var updated *fieldpath.Set
		if entry.Manager == "eno" && entry.Operation == metav1.ManagedFieldsOperationApply {
			updated = set.Union(expected)
		} else {
			updated = set.Difference(expected)
		}

		js, err := updated.ToJSON()
		if err != nil {
			copy = append(copy, entry)
			continue
		}

		entry.FieldsV1 = &metav1.FieldsV1{Raw: js}
		copy = append(copy, entry)
	}

	return copy
}

func parseEnoFields(entries []metav1.ManagedFieldsEntry) *fieldpath.Set {
	for _, entry := range entries {
		if entry.Manager == "eno" && entry.Operation == metav1.ManagedFieldsOperationApply {
			if set := parseFieldsEntry(entry); set != nil {
				return set
			}
		}
	}
	return &fieldpath.Set{}
}

func parseAllFields(entries []metav1.ManagedFieldsEntry) *fieldpath.Set {
	result := &fieldpath.Set{}
	for _, entry := range entries {
		if entry.Manager != "eno" {
			if set := parseFieldsEntry(entry); set != nil {
				result = result.Union(set)
			}
		}
	}
	return result
}

// parseFieldsEntry safely parses a single managed fields entry
func parseFieldsEntry(entry metav1.ManagedFieldsEntry) *fieldpath.Set {
	if entry.FieldsV1 == nil {
		return nil
	}

	set := &fieldpath.Set{}
	err := set.FromJSON(bytes.NewBuffer(entry.FieldsV1.Raw))
	if err != nil {
		return nil
	}
	return set
}

// Compare compares two unstructured resources while ignoring:
// - Managed field metadata not controlled by Eno
// - Resource version
// - Generation
// - Status
//
// This should only be used to compare canonical apiserver representations of the resource
// i.e. the unmodified response from gets or patch/update dryruns.
func Compare(a, b *unstructured.Unstructured) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if !compareEnoManagedFields(a.GetManagedFields(), b.GetManagedFields()) {
		return false
	}
	return equality.Semantic.DeepEqual(stripInsignificantFields(a), stripInsignificantFields(b))
}

// compareEnoManagedFields returns true when the Eno managed fields in both slices are equal.
func compareEnoManagedFields(a, b []metav1.ManagedFieldsEntry) bool {
	cmp := func(cur metav1.ManagedFieldsEntry) bool { return cur.Manager == "eno" }
	ai := slices.IndexFunc(a, cmp)
	ab := slices.IndexFunc(b, cmp)
	if ai == -1 && ab == -1 {
		return true
	}
	if ai == -1 || ab == -1 {
		return false
	}
	return equality.Semantic.DeepEqual(a[ai].FieldsV1, b[ab].FieldsV1)
}

func stripInsignificantFields(u *unstructured.Unstructured) *unstructured.Unstructured {
	u = u.DeepCopy()
	u.SetManagedFields(nil)
	u.SetUID(types.UID(""))
	u.SetResourceVersion("")
	u.SetGeneration(0)
	delete(u.Object, "status")
	return u
}
