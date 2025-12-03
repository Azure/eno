package reconciliation

import (
	"encoding/json"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	enoFieldManager   = "eno"
	initContainerPath = "spec.template.spec.initContainers"
)

// OwnershipStatus
type OwnershipStatus struct {
	FullyOwnedByEno     bool
	OtherManagers       []string
	OtherUpdateManagers []string // Managers using Update operation (cannot be force-migrated)
	ScopeExists         bool
}

// shouldMigrateOwnership checks if the resource type should undergo ownership Migration for eno
// Currently only supports: Deployment
// To-Do: add support for other fields as well
func shouldMigrateOwnership(gvk schema.GroupVersionKind) bool {
	return gvk.Group == "apps" && gvk.Kind == "Deployment"
}

// getInitContainerScope returns the JSON path to initContainers for Deployment
// Returns "spec.template.spec.initContainers" for Deployment
// Returns empty string for unsupported type
func getMigrationScope(gvk schema.GroupVersionKind) string {
	if !shouldMigrateOwnership(gvk) {
		return ""
	}
	return initContainerPath
}

// checkFieldUnderScope checks if any fields exists under the given scope in a managedFields entry.
// scopePath should be ['spec', 'template', 'spec', 'initContainers']
// fieldMap is the parsed fieldsV1 JSON object
func checkFieldUnderScope(fieldsMap map[string]interface{}, scopePath []string) bool {
	if len(scopePath) == 0 {
		return true
	}
	current := fieldsMap
	for i, part := range scopePath {
		// look for the field key (e.g. "f:spec", "f:template")
		fieldKey := "f:" + part
		next, ok := current[fieldKey]
		if !ok {
			return false
		}

		// if this is the last part of the path, we found it
		if i == len(scopePath)-1 {
			return true
		}

		nextMap, ok := next.(map[string]interface{})
		current = nextMap
	}
	return false
}

// CheckOwnership analyzes managedFields to determine owneship status of a scope
// Returns OwnershipStatus indicating if eno fully owns the scope and which other managers (if any ) have ownership
func CheckOwnership(resource *unstructured.Unstructured, scope string, enoManager string) (*OwnershipStatus, error) {
	status := &OwnershipStatus{
		FullyOwnedByEno: false,
		OtherManagers:   []string{},
		ScopeExists:     false,
	}

	// check if the scope exists in the resource
	scopePath := strings.Split(scope, ".")
	_, found, err := unstructured.NestedFieldNoCopy(resource.Object, scopePath...)
	if err != nil {
		return nil, fmt.Errorf("Checking if scope exists: %w", err)
	}
	status.ScopeExists = found

	// If scope does not exists, no migration needed
	if !found {
		status.FullyOwnedByEno = true // nothing to own, field does not exist
		return status, nil
	}

	// Parse managedFields
	managedFields := resource.GetManagedFields()
	if len(managedFields) == 0 {
		// no managed fields, no ssa owneship tracking
		return status, nil
	}

	enoOwnsField := false
	otherApplyManagersMap := make(map[string]bool)
	otherUpdateManagersMap := make(map[string]bool)
	for _, entry := range managedFields {
		// We need to check both Apply and Update operations
		// Apply operations can be force-migrated with SSA + ForceOwnership
		// Update operations cannot be force-migrated and require manual intervention
		if entry.Operation != metav1.ManagedFieldsOperationApply && entry.Operation != metav1.ManagedFieldsOperationUpdate {
			continue
		}

		if entry.FieldsV1 == nil {
			continue
		}
		var fieldsMap map[string]interface{}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fieldsMap); err != nil {
			return nil, fmt.Errorf("parsing fieldsV1 for manager %s: %w", entry.Manager, err)
		}

		// Check if this manager owns fields under the scope
		ownsFieldsinScope := checkFieldUnderScope(fieldsMap, scopePath)
		if ownsFieldsinScope {
			if entry.Manager == enoManager {
				enoOwnsField = true
			} else {
				if entry.Operation == metav1.ManagedFieldsOperationApply {
					otherApplyManagersMap[entry.Manager] = true
				} else {
					otherUpdateManagersMap[entry.Manager] = true
				}
			}
		}
	}

	for manager := range otherApplyManagersMap {
		status.OtherManagers = append(status.OtherManagers, manager)
	}
	for manager := range otherUpdateManagersMap {
		status.OtherUpdateManagers = append(status.OtherUpdateManagers, manager)
	}

	status.FullyOwnedByEno = enoOwnsField && len(status.OtherManagers) == 0 && len(status.OtherUpdateManagers) == 0
	return status, nil
}

// removeScopeFromManagedFields removes all fields under the specified scope from the given managers' managedFields entries.
// This is necessary for Update managers since SSA with ForceOwnership cannot take ownership from Update operations.
// Returns true if managedFields were modified, false otherwise.
func removeScopeFromManagedFields(resource *unstructured.Unstructured, scope string, managers []string) (bool, error) {
	if len(managers) == 0 {
		return false, nil
	}

	scopePath := strings.Split(scope, ".")
	managedFields := resource.GetManagedFields()
	modified := false
	newManagedFields := make([]metav1.ManagedFieldsEntry, 0, len(managedFields))

	for _, entry := range managedFields {
		// Check if this is one of the managers we need to modify
		shouldModify := false
		for _, mgr := range managers {
			if entry.Manager == mgr {
				shouldModify = true
				break
			}
		}

		if !shouldModify {
			newManagedFields = append(newManagedFields, entry)
			continue
		}

		// Parse the fieldsV1
		if entry.FieldsV1 == nil {
			newManagedFields = append(newManagedFields, entry)
			continue
		}

		var fieldsMap map[string]interface{}
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fieldsMap); err != nil {
			return false, fmt.Errorf("parsing fieldsV1 for manager %s: %w", entry.Manager, err)
		}

		// Remove the scope from the fields
		if removeScopeFromFieldsMap(fieldsMap, scopePath) {
			modified = true

			// If the fieldsMap is now empty, skip this entry entirely
			if len(fieldsMap) == 0 || (len(fieldsMap) == 1 && fieldsMap["f:metadata"] != nil) {
				// Only metadata left, remove the entire entry
				continue
			}

			// Marshal back to JSON
			updatedFields, err := json.Marshal(fieldsMap)
			if err != nil {
				return false, fmt.Errorf("marshaling updated fieldsV1 for manager %s: %w", entry.Manager, err)
			}
			entry.FieldsV1.Raw = updatedFields
		}

		newManagedFields = append(newManagedFields, entry)
	}

	if modified {
		resource.SetManagedFields(newManagedFields)
	}

	return modified, nil
}

// removeScopeFromFieldsMap recursively removes the scope path from a fieldsV1 map.
// Returns true if the map was modified.
func removeScopeFromFieldsMap(fieldsMap map[string]interface{}, scopePath []string) bool {
	if len(scopePath) == 0 {
		return false
	}

	// Navigate to the parent of the scope
	current := fieldsMap
	for i := 0; i < len(scopePath)-1; i++ {
		fieldKey := "f:" + scopePath[i]
		next, ok := current[fieldKey]
		if !ok {
			return false // Scope doesn't exist in this manager's fields
		}

		nextMap, ok := next.(map[string]interface{})
		if !ok {
			return false
		}
		current = nextMap
	}

	// Remove the final scope field
	lastFieldKey := "f:" + scopePath[len(scopePath)-1]
	if _, exists := current[lastFieldKey]; exists {
		delete(current, lastFieldKey)
		return true
	}

	return false
}
