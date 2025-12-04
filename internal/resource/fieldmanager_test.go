package resource

import (
	"bytes"
	"encoding/json"
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
			expectModified: true,
			expectManagers: []string{"eno", "kube-controller-manager"},
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
			expectModified: true,
			expectManagers: []string{"eno"},
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
			expectModified: true,
			expectManagers: []string{"eno", "eno", "kube-controller-manager"},
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
			expectModified: true,
			expectManagers: []string{"eno", "eno"},
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
			expectManagers:    []string{"eno", "kube-controller-manager"},
		},
		{
			name: "custom migratingManagers with duplicate of hardcoded manager - should not duplicate",
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
			migratingManagers: []string{"kubectl"},
			expectModified:    true,
			expectManagers:    []string{"eno", "eno"},
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
			expectManagers:    []string{"eno", "eno", "kube-controller-manager"},
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
			expectManagers:    []string{"eno", "eno"},
		},
		{
			name: "empty migratingManagers - should use only hardcoded managers",
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
			expectModified:    true,
			expectManagers:    []string{"eno", "unknown-operator"},
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

			modified, _, err := NormalizeConflictingManagers(obj, tc.migratingManagers)

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
