package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

func TestManagedFields(t *testing.T) {
	tests := []struct {
		Name                    string
		ExpectModified          bool
		Previous, Current, Next []metav1.ManagedFieldsEntry
		Expected                []metav1.ManagedFieldsEntry
	}{
		{
			Name:           "fully matching",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
		},
		{
			Name:           "all eno managed fields lost",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "notEno", []string{"baz"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
		},
		{
			Name:           "all eno managed fields lost, some fields collide with another manager",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz", "foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "notEno", []string{"baz"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "notEno", []string{"baz"}),
			},
		},
		{
			Name:           "field removed, owned by another field manager",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}), // "bar" moved to notEno
				makeFields(t, "notEno", []string{"bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
			},
		},
		{
			Name:           "field removed, already owned by eno",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
		},
		{
			Name:           "field removed, missing from current state",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
		},
		{
			Name:           "empty previous managed fields",
			ExpectModified: false,
			Previous:       []metav1.ManagedFieldsEntry{},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
		},
		{
			Name:           "nil FieldsV1 entries",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   nil,
				},
				makeFields(t, "other", []string{"foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   nil,
				},
				makeFields(t, "other", []string{"foo"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   nil,
				},
				makeFields(t, "other", []string{"foo"}),
			},
		},
		{
			Name:           "non-Apply operation for eno manager",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:foo":{}}`)},
				},
				makeFields(t, "other", []string{"bar"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:foo":{}}`)},
				},
				makeFields(t, "other", []string{"bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:foo":{}}`)},
				},
				makeFields(t, "other", []string{"bar"}),
			},
		},
		{
			Name:           "JSON parsing error in previous fields",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`invalid json`)},
				},
				makeFields(t, "other", []string{"bar"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`invalid json`)},
				},
				makeFields(t, "other", []string{"bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`invalid json`)},
				},
				makeFields(t, "other", []string{"bar"}),
			},
		},
		{
			Name:           "empty next fields",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Next: []metav1.ManagedFieldsEntry{},
		},
		{
			Name:           "special branch: prevEno not empty, nextEno not empty, currentEno empty",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "other", []string{"baz"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "other", []string{"baz"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
				makeFields(t, "other", []string{"baz"}),
			},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
				makeFields(t, "other", []string{"baz"}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			merged, _, modified := MergeEnoManagedFields(tc.Previous, tc.Current, tc.Next)
			assert.Equal(t, tc.ExpectModified, modified)
			assert.Equal(t, parseFieldEntries(tc.Expected), parseFieldEntries(merged))

			// Prove that the current slice wasn't mutated
			if tc.ExpectModified {
				assert.NotEqual(t, tc.Current, merged)
			}
		})
	}
}

func makeFields(t *testing.T, manager string, fields []string) metav1.ManagedFieldsEntry {
	set := &fieldpath.Set{}
	for _, field := range fields {
		set.Insert(fieldpath.MakePathOrDie(field))
	}

	js, err := set.ToJSON()
	require.NoError(t, err)

	entry := metav1.ManagedFieldsEntry{}
	entry.Manager = manager
	entry.FieldsType = "FieldsV1"
	entry.Operation = metav1.ManagedFieldsOperationApply
	entry.FieldsV1 = &metav1.FieldsV1{Raw: js}
	return entry
}

func parseFieldEntries(entries []metav1.ManagedFieldsEntry) []*fieldpath.Set {
	sets := make([]*fieldpath.Set, len(entries))
	for i, entry := range entries {
		if entry.FieldsV1 == nil {
			continue
		}
		set := &fieldpath.Set{}
		err := set.FromJSON(bytes.NewBuffer(entry.FieldsV1.Raw))
		if err != nil {
			continue
		}
		sets[i] = set
	}
	return sets
}

func TestNormalizeConflictingManagers(t *testing.T) {
	tests := []struct {
		name              string
		managedFields     []metav1.ManagedFieldsEntry
		migratingManagers []string
		expectModified    bool
		expectManagers    []string // expected manager names after normalization
	}{
		{
			name: "normalize Go-http-client owning f:spec",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:replicas":{}}}`)},
				},
			},
			migratingManagers: []string{"Go-http-client"},
			expectModified:    true,
			expectManagers:    []string{"kube-controller-manager", "eno"},
		},
		{
			name: "do not normalize manager owning only f:status",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:replicas":{}}}`)},
				},
			},
			expectModified: false,
			expectManagers: []string{"kube-controller-manager"},
		},
		{
			name: "do not normalize manager owning only f:metadata",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:ownerReferences":{}}}`)},
				},
			},
			expectModified: false,
			expectManagers: []string{"kube-controller-manager"},
		},
		{
			name: "normalize manager owning both f:spec and f:metadata",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}},"f:metadata":{"f:labels":{}}}`)},
				},
			},
			migratingManagers: []string{"Go-http-client"},
			expectModified:    true,
			expectManagers:    []string{"eno"},
		},
		{
			name: "skip entry already owned by eno",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
			},
			expectModified: false,
			expectManagers: []string{"eno"},
		},
		{
			name: "normalize multiple managers owning f:spec",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:ready":{}}}`)},
				},
			},
			migratingManagers: []string{"Go-http-client", "kubectl"},
			expectModified:    true,
			expectManagers:    []string{"kube-controller-manager", "eno"},
		},
		{
			name:           "empty managed fields",
			managedFields:  []metav1.ManagedFieldsEntry{},
			expectModified: false,
			expectManagers: []string{},
		},
		{
			name: "nil FieldsV1",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   nil,
				},
			},
			expectModified: false,
			expectManagers: []string{"Go-http-client"},
		},
		{
			name: "eno already owns f:spec, other manager owns f:status only - should not normalize",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:conditions":{}}}`)},
				},
			},
			expectModified: false,
			expectManagers: []string{"eno", "manager"},
		},
		{
			name: "eno already owns f:spec, another eno entry owns different f:spec - should merge into one",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
			},
			expectModified: true,
			expectManagers: []string{"eno"},
		},
		{
			name: "eno already owns f:spec, other manager also owns f:spec - should normalize",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectManagers:    []string{"eno"},
		},
		{
			name: "custom migratingManagers with unique manager",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "custom-operator",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:ready":{}}}`)},
				},
			},
			migratingManagers: []string{"custom-operator"},
			expectModified:    true,
			expectManagers:    []string{"kube-controller-manager", "eno"},
		},
		{
			name: "custom migratingManagers with duplicate manager - should not duplicate",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl", "Go-http-client", "kubectl"},
			expectModified:    true,
			expectManagers:    []string{"eno"},
		},
		{
			name: "custom migratingManagers with multiple new managers",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "operator-a",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "operator-b",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:ready":{}}}`)},
				},
			},
			migratingManagers: []string{"operator-a", "operator-b"},
			expectModified:    true,
			expectManagers:    []string{"kube-controller-manager", "eno"},
		},
		{
			name: "custom migratingManagers with hardcoded and new managers - should merge all unique",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "custom-operator",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
			},
			migratingManagers: []string{"custom-operator", "kubectl"},
			expectModified:    true,
			expectManagers:    []string{"eno"},
		},
		{
			name: "empty migratingManagers - should not normalize",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "unknown-operator",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
			},
			migratingManagers: []string{},
			expectModified:    false,
			expectManagers:    []string{"kubectl", "unknown-operator"},
		},
		{
			name: "migratingManagers with empty strings - should filter out empty strings",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "custom-operator",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
			},
			migratingManagers: []string{"", "custom-operator", ""},
			expectModified:    true,
			expectManagers:    []string{"eno"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetManagedFields(tc.managedFields)

			modified, err := NormalizeConflictingManagers(context.Background(), obj, tc.migratingManagers)

			require.NoError(t, err)
			assert.Equal(t, tc.expectModified, modified)

			actualManagers := make([]string, 0)
			for _, entry := range obj.GetManagedFields() {
				actualManagers = append(actualManagers, entry.Manager)
			}
			assert.Equal(t, tc.expectManagers, actualManagers)

			// If modified, verify that managers owning f:spec have operation changed to Apply
			if tc.expectModified {
				for _, entry := range obj.GetManagedFields() {
					if entry.Manager == "eno" && entry.FieldsV1 != nil {
						fieldsV1 := make(map[string]interface{})
						err := json.Unmarshal(entry.FieldsV1.Raw, &fieldsV1)
						require.NoError(t, err)
						if _, hasSpec := fieldsV1["f:spec"]; hasSpec {
							assert.Equal(t, metav1.ManagedFieldsOperationApply, entry.Operation)
						}
					}
				}
			}
		})
	}
}

// parseFieldPath converts a path string to field components with "f:" prefix
// Examples: ".spec.replicas" -> ["f:spec", "f:replicas"]
//
//	"f:spec.f:replicas" -> ["f:spec", "f:replicas"]
func parseFieldPath(path string) []string {
	// Remove leading dot if present
	path = strings.TrimPrefix(path, ".")

	// Split by dots
	parts := strings.Split(path, ".")

	var components []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		// Add "f:" prefix if not already present
		if !strings.HasPrefix(part, "f:") {
			part = "f:" + part
		}
		components = append(components, part)
	}
	return components
}

// hasNestedField checks if a nested field path exists in the fieldsMap
// Example: hasNestedField(map, ["f:spec", "f:replicas"]) checks if map["f:spec"]["f:replicas"] exists
func hasNestedField(fieldsMap map[string]interface{}, components []string) bool {
	if len(components) == 0 {
		return false
	}

	current := fieldsMap
	for i, component := range components {
		val, exists := current[component]
		if !exists {
			return false
		}

		// If this is the last component, we're done
		if i == len(components)-1 {
			return true
		}

		// Otherwise, expect a nested map
		nextMap, ok := val.(map[string]interface{})
		if !ok {
			return false
		}
		current = nextMap
	}

	return true
}

func TestNormalizeConflictingManagers_FieldMerging(t *testing.T) {
	tests := []struct {
		name              string
		managedFields     []metav1.ManagedFieldsEntry
		migratingManagers []string
		expectModified    bool
		// Verify that specific fields are present in the merged eno entry
		expectEnoFields []string
	}{
		{
			name: "merge two eno entries with different fields",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{"f:metadata":{}}}}`)},
				},
			},
			expectModified:  true,
			expectEnoFields: []string{".spec", ".spec.replicas", ".spec.template", ".spec.template.metadata"},
		},
		{
			name: "merge legacy manager fields into existing eno entry",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:selector":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectEnoFields:   []string{".spec", ".spec.replicas", ".spec.selector"},
		},
		{
			name: "merge multiple legacy managers into eno",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{}}}`)},
				},
				{
					Manager:    "custom-operator",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:labels":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl", "Go-http-client", "custom-operator"},
			expectModified:    true,
			expectEnoFields:   []string{".spec", ".spec.replicas", ".spec.template", ".metadata", ".metadata.labels"},
		},
		{
			name: "merge eno entries and legacy managers together",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:paused":{}}}`)},
				},
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:selector":{}}}`)},
				},
				{
					Manager:    "Go-http-client",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:metadata":{"f:annotations":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl", "Go-http-client"},
			expectModified:    true,
			expectEnoFields:   []string{".spec", ".spec.replicas", ".spec.paused", ".spec.selector", ".metadata", ".metadata.annotations"},
		},
		{
			name: "only merge specified legacy managers, not others",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
				{
					Manager:    "kube-controller-manager",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:status":{"f:ready":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectEnoFields:   []string{".spec", ".spec.replicas"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetManagedFields(tc.managedFields)

			modified, err := NormalizeConflictingManagers(context.Background(), obj, tc.migratingManagers)

			require.NoError(t, err)
			assert.Equal(t, tc.expectModified, modified)

			// Find the eno entry
			var enoEntry *metav1.ManagedFieldsEntry
			for _, entry := range obj.GetManagedFields() {
				if entry.Manager == "eno" && entry.Operation == metav1.ManagedFieldsOperationApply {
					enoEntry = &entry
					break
				}
			}

			require.NotNil(t, enoEntry, "expected to find an eno managed fields entry")
			require.NotNil(t, enoEntry.FieldsV1, "expected eno entry to have FieldsV1")

			// Deserialize the FieldsV1 JSON to verify all expected field components exist
			var fieldsMap map[string]interface{}
			err = json.Unmarshal(enoEntry.FieldsV1.Raw, &fieldsMap)
			require.NoError(t, err)

			// Verify all expected fields are present in the JSON structure
			for _, expectedPath := range tc.expectEnoFields {
				// Convert path like ".spec.replicas" to ["f:spec", "f:replicas"]
				// or "f:spec.f:replicas" to ["f:spec", "f:replicas"]
				components := parseFieldPath(expectedPath)
				assert.True(t, hasNestedField(fieldsMap, components),
					"expected eno entry to contain field path: %s (components: %v)", expectedPath, components)
			}

			// Verify that migrated managers are no longer present
			for _, entry := range obj.GetManagedFields() {
				for _, migratingMgr := range tc.migratingManagers {
					if migratingMgr != "" && entry.Manager == migratingMgr {
						t.Errorf("expected legacy manager %s to be removed, but it's still present", migratingMgr)
					}
				}
			}
		})
	}
}

func TestNormalizeConflictingManagers_AnnotationGating(t *testing.T) {
	tests := []struct {
		name              string
		annotations       map[string]string
		managedFields     []metav1.ManagedFieldsEntry
		migratingManagers []string
		expectModified    bool
		expectAnnotation  bool
	}{
		{
			name: "no-op when migration annotation present with v1",
			annotations: map[string]string{
				"eno.azure.com/ownership-migrated": "v1",
			},
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    false,
			expectAnnotation:  true, // annotation should remain
		},
		{
			name:        "migration occurs and sets annotation when not present",
			annotations: nil,
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectAnnotation:  true, // annotation should be set
		},
		{
			name: "migration occurs when annotation present with wrong value",
			annotations: map[string]string{
				"eno.azure.com/ownership-migrated": "v0",
			},
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectAnnotation:  true, // annotation should be updated to v1
		},
		{
			name: "no-op when no conflicting managers present - no annotation set",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    false,
			expectAnnotation:  false, // no migration occurred, no annotation set
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetManagedFields(tc.managedFields)
			if tc.annotations != nil {
				obj.SetAnnotations(tc.annotations)
			}

			modified, err := NormalizeConflictingManagers(context.Background(), obj, tc.migratingManagers)

			require.NoError(t, err)
			assert.Equal(t, tc.expectModified, modified)

			annotations := obj.GetAnnotations()
			if tc.expectAnnotation {
				require.NotNil(t, annotations)
				assert.Equal(t, "v1", annotations["eno.azure.com/ownership-migrated"])
			} else {
				if annotations != nil {
					_, exists := annotations["eno.azure.com/ownership-migrated"]
					assert.False(t, exists, "annotation should not be set when no migration occurs")
				}
			}
		})
	}
}

func TestNormalizeConflictingManagers_FieldPathFiltering(t *testing.T) {
	tests := []struct {
		name                  string
		managedFields         []metav1.ManagedFieldsEntry
		migratingManagers     []string
		expectModified        bool
		expectEnoFields       []string
		expectLegacyStillOwns []string
	}{
		{
			name: "only spec, metadata.labels, metadata.annotations migrated",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}, "f:metadata":{"f:labels":{"f:app":{}}, "f:annotations":{"f:note":{}}, "f:finalizers":{}}}`)},
				},
			},
			migratingManagers:     []string{"kubectl"},
			expectModified:        true,
			expectEnoFields:       []string{".spec.replicas", ".metadata.labels.app", ".metadata.annotations.note"},
			expectLegacyStillOwns: []string{".metadata.finalizers"},
		},
		{
			name: "status fields not migrated",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}, "f:status":{"f:ready":{}}}`)},
				},
			},
			migratingManagers:     []string{"kubectl"},
			expectModified:        true,
			expectEnoFields:       []string{".spec.replicas"},
			expectLegacyStillOwns: []string{".status.ready"},
		},
		{
			name: "deletionTimestamp not migrated",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}, "f:metadata":{"f:deletionTimestamp":{}}}`)},
				},
			},
			migratingManagers:     []string{"kubectl"},
			expectModified:        true,
			expectEnoFields:       []string{".spec.replicas"},
			expectLegacyStillOwns: []string{".metadata.deletionTimestamp"},
		},
		{
			name: "multiple eno entries merged with filtering",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}, "f:status":{"f:ready":{}}}`)},
				},
				{
					Manager:    "eno",
					Operation:  metav1.ManagedFieldsOperationApply,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:paused":{}}, "f:metadata":{"f:finalizers":{}}}`)},
				},
			},
			migratingManagers:     []string{},
			expectModified:        true,
			expectEnoFields:       []string{".spec.replicas", ".spec.paused"},
			expectLegacyStillOwns: []string{}, // status and finalizers are filtered out entirely
		},
		{
			name: "complex nested spec paths migrated",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:template":{"f:spec":{"f:containers":{}}}}}`)},
				},
			},
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectEnoFields:   []string{".spec.template.spec.containers"},
		},
		{
			name: "other metadata fields not migrated",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:    "kubectl",
					Operation:  metav1.ManagedFieldsOperationUpdate,
					FieldsType: "FieldsV1",
					FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}, "f:metadata":{"f:ownerReferences":{},"f:resourceVersion":{}}}`)},
				},
			},
			migratingManagers:     []string{"kubectl"},
			expectModified:        true,
			expectEnoFields:       []string{".spec.replicas"},
			expectLegacyStillOwns: []string{".metadata.ownerReferences", ".metadata.resourceVersion"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{}
			obj.SetManagedFields(tc.managedFields)

			modified, err := NormalizeConflictingManagers(context.Background(), obj, tc.migratingManagers)

			require.NoError(t, err)
			assert.Equal(t, tc.expectModified, modified)

			// Find the eno entry
			var enoEntry *metav1.ManagedFieldsEntry
			for _, entry := range obj.GetManagedFields() {
				if entry.Manager == "eno" && entry.Operation == metav1.ManagedFieldsOperationApply {
					enoEntry = &entry
					break
				}
			}

			require.NotNil(t, enoEntry, "expected to find an eno managed fields entry")
			require.NotNil(t, enoEntry.FieldsV1, "expected eno entry to have FieldsV1")

			// Deserialize the FieldsV1 JSON to verify expected fields exist
			var enoFieldsMap map[string]interface{}
			err = json.Unmarshal(enoEntry.FieldsV1.Raw, &enoFieldsMap)
			require.NoError(t, err)

			// Verify all expected fields are present in eno entry
			for _, expectedPath := range tc.expectEnoFields {
				components := parseFieldPath(expectedPath)
				assert.True(t, hasNestedField(enoFieldsMap, components),
					"expected eno entry to contain field path: %s (components: %v)", expectedPath, components)
			}

			// Verify excluded fields are NOT in eno entry
			for _, excludedPath := range tc.expectLegacyStillOwns {
				components := parseFieldPath(excludedPath)
				assert.False(t, hasNestedField(enoFieldsMap, components),
					"expected eno entry to NOT contain field path: %s (components: %v)", excludedPath, components)
			}

			// Verify that legacy managers still own the excluded fields (if any)
			if len(tc.expectLegacyStillOwns) > 0 && len(tc.migratingManagers) > 0 {
				// Find any remaining legacy manager entries (they should still exist with excluded fields)
				for _, entry := range obj.GetManagedFields() {
					for _, migratingMgr := range tc.migratingManagers {
						if entry.Manager == migratingMgr && entry.FieldsV1 != nil {
							var legacyFieldsMap map[string]interface{}
							err = json.Unmarshal(entry.FieldsV1.Raw, &legacyFieldsMap)
							require.NoError(t, err)

							// Verify the legacy manager still owns at least one excluded field
							for _, excludedPath := range tc.expectLegacyStillOwns {
								components := parseFieldPath(excludedPath)
								if hasNestedField(legacyFieldsMap, components) {
									// Good - at least one excluded field is still owned by legacy manager
									return
								}
							}
						}
					}
				}
			}
		})
	}
}
