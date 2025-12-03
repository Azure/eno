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
