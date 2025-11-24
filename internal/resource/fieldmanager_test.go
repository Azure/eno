package resource

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			merged, _, modified := MergeEnoManagedFields(tc.Previous, tc.Current, tc.Next, nil)
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

func TestMigratingFieldManagers(t *testing.T) {
	tests := []struct {
		Name                    string
		ExpectModified          bool
		Previous, Current, Next []metav1.ManagedFieldsEntry
		MigratingManagers       []string
		Expected                []metav1.ManagedFieldsEntry
	}{
		{
			Name:           "take ownership from single migrating manager",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
				makeFields(t, "legacy-tool", []string{"bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			MigratingManagers: []string{"legacy-tool"},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar"}),
			},
		},
		{
			Name:           "take ownership from multiple migrating managers",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
				makeFields(t, "legacy-tool-1", []string{"bar"}),
				makeFields(t, "legacy-tool-2", []string{"baz"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			MigratingManagers: []string{"legacy-tool-1", "legacy-tool-2"},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "bar", "baz"}),
			},
		},
		{
			Name:           "no migrating managers configured",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
				makeFields(t, "other-tool", []string{"bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			MigratingManagers: nil,
		},
		{
			Name:           "migrating manager not present in current",
			ExpectModified: false,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			MigratingManagers: []string{"legacy-tool"},
		},
		{
			Name:           "take ownership from migrating manager and handle drift",
			ExpectModified: true,
			Previous: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "removed"}),
			},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
				makeFields(t, "drift-tool", []string{"removed"}),
				makeFields(t, "legacy-tool", []string{"bar"}),
			},
			Next: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo"}),
			},
			MigratingManagers: []string{"legacy-tool"},
			Expected: []metav1.ManagedFieldsEntry{
				makeFields(t, "eno", []string{"foo", "removed", "bar"}),
			},
		},
		{
			Name:           "empty previous eno fields with migrating manager",
			ExpectModified: false,
			Previous:       []metav1.ManagedFieldsEntry{},
			Current: []metav1.ManagedFieldsEntry{
				makeFields(t, "legacy-tool", []string{"bar"}),
			},
			Next:              []metav1.ManagedFieldsEntry{},
			MigratingManagers: []string{"legacy-tool"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			merged, _, modified := MergeEnoManagedFields(tc.Previous, tc.Current, tc.Next, tc.MigratingManagers)
			assert.Equal(t, tc.ExpectModified, modified)
			if tc.Expected != nil {
				assert.Equal(t, parseFieldEntries(tc.Expected), parseFieldEntries(merged))
			}

			// Prove that the current slice wasn't mutated
			if tc.ExpectModified {
				assert.NotEqual(t, tc.Current, merged)
			}
		})
	}
}
