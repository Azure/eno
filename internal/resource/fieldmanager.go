package resource

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

const (
	enoManager               = "eno"
	ownershipMigratedAnnoKey = "eno.azure.com/ownership-migrated"
	ownershipMigratedVersion = "v1"
)

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

func NormalizeConflictingManagers(ctx context.Context, current *unstructured.Unstructured, migratingManagers []string) (modified bool, err error) {
	// Check for migration annotation - if present, this object has already been migrated
	annotations := current.GetAnnotations()
	if annotations != nil && annotations[ownershipMigratedAnnoKey] == ownershipMigratedVersion {
		return false, nil // Already migrated, skip
	}

	managedFields := current.GetManagedFields()
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("NormalizingConflictingManager", "Name", current.GetName(), "Namespace", current.GetNamespace())
	if len(managedFields) == 0 {
		return false, nil
	}

	// Build the unique list of managers to migrate from user-provided migratingManagers
	uniqueMigratingManagers := buildUniqueManagersList(migratingManagers)

	// Check if normalization is needed
	hasLegacyManager, enoEntryCount, err := analyzeManagerConflicts(managedFields, uniqueMigratingManagers)
	if err != nil {
		return false, err
	}
	// Skip normalization if there are no legacy managers and at most one eno entry
	if !hasLegacyManager && enoEntryCount <= 1 {
		return false, nil
	}

	// Merge all eno entries first to get the combined fieldset
	mergedEnoSet, mergedEnoTime := mergeEnoEntries(managedFields)

	// Build new managedFields list, merging legacy managers into eno and excluding original eno entries
	newManagedFields := make([]metav1.ManagedFieldsEntry, 0, len(managedFields))
	modified = false

	for i := range managedFields {
		entry := &managedFields[i]

		// Skip eno Apply entries - they will be merged into one entry later
		if entry.Manager == enoManager && entry.Operation == metav1.ManagedFieldsOperationApply {
			modified = true
			continue
		}

		// Keep entries without fieldsV1 as-is
		if entry.FieldsV1 == nil {
			newManagedFields = append(newManagedFields, *entry)
			continue
		}

		// keep non-eno, non-legacy managers as is
		if !uniqueMigratingManagers[entry.Manager] {
			logger.Info("NormalizeConflictingManagers non-eno and non-legacy manager found, skipping normalizing", "manager", entry.Manager,
				"resourceName", current.GetName(), "resourceNamespace", current.GetNamespace())
			newManagedFields = append(newManagedFields, *entry)
			continue
		}

		logger.Info("NormalizeConflictingManagers found migrating managers", "manager", entry.Manager,
			"resoruceName", current.GetName(), "resourceNamespace", current.GetNamespace())
		// Check if this is a legacy manager that should be migrated to eno
		// Separate allowed fields (to migrate) from excluded fields (to keep with legacy manager)
		if mergedEnoSet == nil {
			mergedEnoSet = &fieldpath.Set{}
		}
		if set := parseFieldsEntry(*entry); set != nil {
			// Filter to only include allowed field paths for migration
			allowedFields := filterAllowedFieldPaths(set)
			if !allowedFields.Empty() {
				mergedEnoSet = mergedEnoSet.Union(allowedFields)
			}

			// Keep the legacy manager entry if it has excluded fields
			excludedFields := set.Difference(allowedFields)
			if !excludedFields.Empty() {
				// Create a new entry with only the excluded fields
				js, err := excludedFields.ToJSON()
				if err == nil {
					entryCopy := *entry
					entryCopy.FieldsV1 = &metav1.FieldsV1{Raw: js}
					newManagedFields = append(newManagedFields, entryCopy)
				}
			}
		}
		// Update the timestamp to the most recent
		if mergedEnoTime == nil || (entry.Time != nil && entry.Time.After(mergedEnoTime.Time)) {
			mergedEnoTime = entry.Time
		}
		modified = true
	}

	// Add the merged eno entry if we found any eno entries OR migrated any legacy manager fields
	if mergedEnoSet != nil && !mergedEnoSet.Empty() {
		mergedEntry, err := createMergedEnoEntry(mergedEnoSet, mergedEnoTime, managedFields)
		if err != nil {
			return false, err
		}
		newManagedFields = append(newManagedFields, mergedEntry)
		modified = true // Ensure modified is true when we create/update the eno entry
	}

	if modified {
		current.SetManagedFields(newManagedFields)
		// Set the annotation to prevent re-running migration
		annotations := current.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[ownershipMigratedAnnoKey] = ownershipMigratedVersion
		current.SetAnnotations(annotations)
	}

	return modified, nil
}

// buildUniqueManagersList creates a deduplicated map from the migratingManagers slice.
// Returns a map of all managers that should be migrated to eno.
func buildUniqueManagersList(migratingManagers []string) map[string]bool {
	unique := make(map[string]bool)

	// Add user-provided managers (duplicates are automatically handled by map)
	for _, manager := range migratingManagers {
		if manager != "" {
			unique[manager] = true
		}
	}

	return unique
}

// analyzeManagerConflicts checks if there are legacy managers present
// and counts the number of eno entries
func analyzeManagerConflicts(managedFields []metav1.ManagedFieldsEntry, uniqueMigratingManagers map[string]bool) (hasLegacyManager bool, enoEntryCount int, err error) {
	for i := range managedFields {
		entry := &managedFields[i]

		if entry.Manager == enoManager {
			enoEntryCount++
			continue
		}

		// Check if this is a legacy manager we need to normalize
		if uniqueMigratingManagers[entry.Manager] {
			hasLegacyManager = true
		}
	}

	return hasLegacyManager, enoEntryCount, nil
}

// mergeEnoEntries merges all eno Apply entries into a single fieldpath.Set
// and tracks the most recent timestamp. Filters the result to only include allowed field paths.
func mergeEnoEntries(managedFields []metav1.ManagedFieldsEntry) (*fieldpath.Set, *metav1.Time) {
	var mergedSet *fieldpath.Set
	var latestTime *metav1.Time

	for i := range managedFields {
		entry := &managedFields[i]

		if entry.Manager == enoManager && entry.Operation == metav1.ManagedFieldsOperationApply {
			if mergedSet == nil {
				mergedSet = &fieldpath.Set{}
			}
			if set := parseFieldsEntry(*entry); set != nil {
				// Filter to only include allowed field paths
				filteredSet := filterAllowedFieldPaths(set)
				if !filteredSet.Empty() {
					mergedSet = mergedSet.Union(filteredSet)
				}
			}
			if latestTime == nil || (entry.Time != nil && entry.Time.After(latestTime.Time)) {
				latestTime = entry.Time
			}
		}
	}

	return mergedSet, latestTime
}

// createMergedEnoEntry creates a single managedFields entry from the merged eno fieldpath.Set
func createMergedEnoEntry(mergedSet *fieldpath.Set, timestamp *metav1.Time, managedFields []metav1.ManagedFieldsEntry) (metav1.ManagedFieldsEntry, error) {
	js, err := mergedSet.ToJSON()
	if err != nil {
		return metav1.ManagedFieldsEntry{}, fmt.Errorf("failed to serialize merged eno fields: %w", err)
	}

	// Find an existing eno entry to use as a template for apiVersion and fieldsType
	var apiVersion string
	var fieldsType string
	for i := range managedFields {
		if managedFields[i].Manager == enoManager {
			apiVersion = managedFields[i].APIVersion
			fieldsType = managedFields[i].FieldsType
			break
		}
	}
	if fieldsType == "" {
		fieldsType = "FieldsV1"
	}

	return metav1.ManagedFieldsEntry{
		Manager:    enoManager,
		Operation:  metav1.ManagedFieldsOperationApply,
		APIVersion: apiVersion,
		Time:       timestamp,
		FieldsType: fieldsType,
		FieldsV1:   &metav1.FieldsV1{Raw: js},
	}, nil
}

// filterAllowedFieldPaths filters a fieldpath.Set to only include paths that are safe to migrate.
// Allowed paths: spec.*, metadata.labels.*, metadata.annotations.*
// Excluded paths: metadata.finalizers, metadata.deletionTimestamp, status.*, and other metadata fields
func filterAllowedFieldPaths(set *fieldpath.Set) *fieldpath.Set {
	if set == nil || set.Empty() {
		return &fieldpath.Set{}
	}

	allowedPrefixes := []fieldpath.Path{
		fieldpath.MakePathOrDie("spec"),
		fieldpath.MakePathOrDie("metadata", "labels"),
		fieldpath.MakePathOrDie("metadata", "annotations"),
	}

	filtered := &fieldpath.Set{}
	set.Iterate(func(path fieldpath.Path) {
		for _, prefix := range allowedPrefixes {
			if hasPrefix(path, prefix) {
				filtered.Insert(path)
				break
			}
		}
	})

	return filtered
}

// hasPrefix checks if a path starts with the given prefix
func hasPrefix(path, prefix fieldpath.Path) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i, elem := range prefix {
		if !path[i].Equals(elem) {
			return false
		}
	}
	return true
}
