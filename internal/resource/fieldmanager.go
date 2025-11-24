package resource

import (
	"bytes"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

// MergeEnoManagedFields corrects managed fields drift to ensure Eno can remove fields
// that are no longer set by the synthesizer, even when another client corrupts the
// managed fields metadata. It also takes ownership of fields from migrating field managers
// when specified. Returns corrected managed fields, affected field paths,
// and whether correction was needed.
func MergeEnoManagedFields(prev, current, next []metav1.ManagedFieldsEntry, migratingFieldManagers []string) (copy []metav1.ManagedFieldsEntry, fields string, modified bool) {
	prevEnoSet := parseEnoFields(prev)
	nextEnoSet := parseEnoFields(next)
	currentEnoSet := parseEnoFields(current)

	// Only perform migration if Eno has previously managed this resource
	if prevEnoSet.Empty() {
		return nil, "", false
	}

	// Check for fields to take from migrating field managers
	migratingFields := parseMigratingFields(current, migratingFieldManagers)

	var expectedFields *fieldpath.Set

	// Handle the drift correction logic
	if !nextEnoSet.Empty() && currentEnoSet.Empty() {
		expectedFields = prevEnoSet
	} else {
		driftFields := prevEnoSet.Difference(nextEnoSet)
		if !driftFields.Empty() {
			driftFields = driftFields.Intersection(parseAllFields(current))
			if !driftFields.Empty() {
				expectedFields = driftFields
			}
		}
	}

	// Add fields from migrating field managers
	if !migratingFields.Empty() {
		if expectedFields == nil {
			expectedFields = migratingFields
		} else {
			expectedFields = expectedFields.Union(migratingFields)
		}
	}

	if expectedFields == nil || expectedFields.Empty() {
		return nil, "", false
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

func parseMigratingFields(entries []metav1.ManagedFieldsEntry, migratingFieldManagers []string) *fieldpath.Set {
	result := &fieldpath.Set{}
	if len(migratingFieldManagers) == 0 {
		return result
	}
	for _, entry := range entries {
		if !slices.Contains(migratingFieldManagers, entry.Manager) {
			continue
		}
		if set := parseFieldsEntry(entry); set != nil {
			result = result.Union(set)
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
