package reconciliation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestShouldMigrateOwnership(t *testing.T) {
	tests := []struct {
		name     string
		gvk      schema.GroupVersionKind
		expected bool
	}{
		{
			name: "Deployment should migrate",
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
			expected: true,
		},
		{
			name: "StatefulSet should not migrate",
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			},
			expected: false,
		},
		{
			name: "Service should not migrate",
			gvk: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Service",
			},
			expected: false,
		},
		{
			name: "ConfigMap should not migrate",
			gvk: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "ConfigMap",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldMigrateOwnership(tt.gvk)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetMigrationScope(t *testing.T) {
	tests := []struct {
		name     string
		gvk      schema.GroupVersionKind
		expected string
	}{
		{
			name: "Deployment returns initContainers path",
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "Deployment",
			},
			expected: "spec.template.spec.initContainers",
		},
		{
			name: "StatefulSet returns empty",
			gvk: schema.GroupVersionKind{
				Group:   "apps",
				Version: "v1",
				Kind:    "StatefulSet",
			},
			expected: "",
		},
		{
			name: "Service returns empty",
			gvk: schema.GroupVersionKind{
				Group:   "",
				Version: "v1",
				Kind:    "Service",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getMigrationScope(tt.gvk)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCheckFieldUnderScope(t *testing.T) {
	tests := []struct {
		name      string
		fieldsMap map[string]interface{}
		scopePath []string
		expected  bool
	}{
		{
			name: "empty scope path returns true",
			fieldsMap: map[string]interface{}{
				"f:spec": map[string]interface{}{},
			},
			scopePath: []string{},
			expected:  true,
		},
		{
			name: "field exists under scope",
			fieldsMap: map[string]interface{}{
				"f:spec": map[string]interface{}{
					"f:template": map[string]interface{}{
						"f:spec": map[string]interface{}{
							"f:initContainers": map[string]interface{}{},
						},
					},
				},
			},
			scopePath: []string{"spec", "template", "spec", "initContainers"},
			expected:  true,
		},
		{
			name: "field does not exist under scope",
			fieldsMap: map[string]interface{}{
				"f:spec": map[string]interface{}{
					"f:template": map[string]interface{}{},
				},
			},
			scopePath: []string{"spec", "template", "spec", "initContainers"},
			expected:  false,
		},
		{
			name: "partial path exists",
			fieldsMap: map[string]interface{}{
				"f:spec": map[string]interface{}{
					"f:replicas": map[string]interface{}{},
				},
			},
			scopePath: []string{"spec", "template"},
			expected:  false,
		},
		{
			name: "single level path exists",
			fieldsMap: map[string]interface{}{
				"f:metadata": map[string]interface{}{
					"f:labels": map[string]interface{}{},
				},
			},
			scopePath: []string{"metadata"},
			expected:  true,
		},
		{
			name: "single level path does not exist",
			fieldsMap: map[string]interface{}{
				"f:spec": map[string]interface{}{},
			},
			scopePath: []string{"metadata"},
			expected:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := checkFieldUnderScope(tt.fieldsMap, tt.scopePath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCheckOwnership(t *testing.T) {
	tests := []struct {
		name          string
		resource      *unstructured.Unstructured
		scope         string
		enoManager    string
		expected      *OwnershipStatus
		expectError   bool
		errorContains string
	}{
		{
			name: "scope does not exist - no migration needed",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name": "test-deployment",
					},
					"spec": map[string]interface{}{
						"replicas": 1,
					},
				},
			},
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: true,
				OtherManagers:   []string{},
				ScopeExists:     false,
			},
			expectError: false,
		},
		{
			name: "scope exists - eno fully owns - no migration needed",
			resource: func() *unstructured.Unstructured {
				fieldsV1Eno := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:template": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:initContainers": map[string]interface{}{},
							},
						},
					},
				}
				fieldsV1EnoBytes, _ := json.Marshal(fieldsV1Eno)

				return &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name": "test-deployment",
							"managedFields": []interface{}{
								map[string]interface{}{
									"manager":   "eno",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1EnoBytes,
									},
								},
							},
						},
						"spec": map[string]interface{}{
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"initContainers": []interface{}{
										map[string]interface{}{
											"name":  "init",
											"image": "busybox",
										},
									},
								},
							},
						},
					},
				}
			}(),
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: true,
				OtherManagers:   []string{},
				ScopeExists:     true,
			},
			expectError: false,
		},
		{
			name: "scope exists - other manager owns - migration needed",
			resource: func() *unstructured.Unstructured {
				fieldsV1 := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:template": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:initContainers": map[string]interface{}{},
							},
						},
					},
				}
				fieldsV1Bytes, _ := json.Marshal(fieldsV1)

				return &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name": "test-deployment",
							"managedFields": []interface{}{
								map[string]interface{}{
									"manager":   "helm",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1Bytes,
									},
								},
							},
						},
						"spec": map[string]interface{}{
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"initContainers": []interface{}{
										map[string]interface{}{
											"name":  "init",
											"image": "busybox",
										},
									},
								},
							},
						},
					},
				}
			}(),
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: false,
				OtherManagers:   []string{"helm"},
				ScopeExists:     true,
			},
			expectError: false,
		},
		{
			name: "scope exists - eno and other manager both own - migration needed",
			resource: func() *unstructured.Unstructured {
				fieldsV1Eno := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:template": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:initContainers": map[string]interface{}{},
							},
						},
					},
				}
				fieldsV1EnoBytes, _ := json.Marshal(fieldsV1Eno)

				fieldsV1Helm := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:template": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:initContainers": map[string]interface{}{},
							},
						},
					},
				}
				fieldsV1HelmBytes, _ := json.Marshal(fieldsV1Helm)

				return &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name": "test-deployment",
							"managedFields": []interface{}{
								map[string]interface{}{
									"manager":   "eno",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1EnoBytes,
									},
								},
								map[string]interface{}{
									"manager":   "helm",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1HelmBytes,
									},
								},
							},
						},
						"spec": map[string]interface{}{
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"initContainers": []interface{}{
										map[string]interface{}{
											"name":  "init",
											"image": "busybox",
										},
									},
								},
							},
						},
					},
				}
			}(),
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: false,
				OtherManagers:   []string{"helm"},
				ScopeExists:     true,
			},
			expectError: false,
		},
		{
			name: "scope exists - multiple other managers own - migration needed",
			resource: func() *unstructured.Unstructured {
				fieldsV1 := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:template": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:initContainers": map[string]interface{}{},
							},
						},
					},
				}
				fieldsV1Bytes, _ := json.Marshal(fieldsV1)

				return &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name": "test-deployment",
							"managedFields": []interface{}{
								map[string]interface{}{
									"manager":   "helm",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1Bytes,
									},
								},
								map[string]interface{}{
									"manager":   "kubectl",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1Bytes,
									},
								},
							},
						},
						"spec": map[string]interface{}{
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"initContainers": []interface{}{
										map[string]interface{}{
											"name":  "init",
											"image": "busybox",
										},
									},
								},
							},
						},
					},
				}
			}(),
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: false,
				OtherManagers:   []string{"helm", "kubectl"},
				ScopeExists:     true,
			},
			expectError: false,
		},
		{
			name: "no managed fields - scope exists",
			resource: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name": "test-deployment",
					},
					"spec": map[string]interface{}{
						"template": map[string]interface{}{
							"spec": map[string]interface{}{
								"initContainers": []interface{}{
									map[string]interface{}{
										"name":  "init",
										"image": "busybox",
									},
								},
							},
						},
					},
				},
			},
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: false,
				OtherManagers:   []string{},
				ScopeExists:     true,
			},
			expectError: false,
		},
		{
			name: "managed fields with Update operation only - should be ignored",
			resource: func() *unstructured.Unstructured {
				fieldsV1 := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:template": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:initContainers": map[string]interface{}{},
							},
						},
					},
				}
				fieldsV1Bytes, _ := json.Marshal(fieldsV1)

				return &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name": "test-deployment",
							"managedFields": []interface{}{
								map[string]interface{}{
									"manager":   "kubectl",
									"operation": "Update",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1Bytes,
									},
								},
							},
						},
						"spec": map[string]interface{}{
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"initContainers": []interface{}{
										map[string]interface{}{
											"name":  "init",
											"image": "busybox",
										},
									},
								},
							},
						},
					},
				}
			}(),
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: false,
				OtherManagers:   []string{},
				ScopeExists:     true,
			},
			expectError: false,
		},
		{
			name: "manager owns different field - not in scope",
			resource: func() *unstructured.Unstructured {
				fieldsV1 := map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:replicas": map[string]interface{}{},
					},
				}
				fieldsV1Bytes, _ := json.Marshal(fieldsV1)

				return &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name": "test-deployment",
							"managedFields": []interface{}{
								map[string]interface{}{
									"manager":   "kubectl",
									"operation": "Apply",
									"fieldsV1": map[string]interface{}{
										"Raw": fieldsV1Bytes,
									},
								},
							},
						},
						"spec": map[string]interface{}{
							"replicas": 3,
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"initContainers": []interface{}{
										map[string]interface{}{
											"name":  "init",
											"image": "busybox",
										},
									},
								},
							},
						},
					},
				}
			}(),
			scope:      "spec.template.spec.initContainers",
			enoManager: "eno",
			expected: &OwnershipStatus{
				FullyOwnedByEno: false,
				OtherManagers:   []string{},
				ScopeExists:     true,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert managedFields from map to proper ManagedFieldsEntry slice if present
			if managedFieldsRaw, ok := tt.resource.Object["metadata"].(map[string]interface{})["managedFields"]; ok {
				managedFieldsList := managedFieldsRaw.([]interface{})
				managedFields := make([]metav1.ManagedFieldsEntry, 0, len(managedFieldsList))
				for _, mf := range managedFieldsList {
					mfMap := mf.(map[string]interface{})
					entry := metav1.ManagedFieldsEntry{
						Manager:   mfMap["manager"].(string),
						Operation: metav1.ManagedFieldsOperationType(mfMap["operation"].(string)),
					}
					if fieldsV1, ok := mfMap["fieldsV1"].(map[string]interface{}); ok {
						if raw, ok := fieldsV1["Raw"].([]byte); ok {
							entry.FieldsV1 = &metav1.FieldsV1{Raw: raw}
						}
					}
					managedFields = append(managedFields, entry)
				}
				tt.resource.SetManagedFields(managedFields)
			}

			result, err := CheckOwnership(tt.resource, tt.scope, tt.enoManager)

			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected.FullyOwnedByEno, result.FullyOwnedByEno)
				assert.Equal(t, tt.expected.ScopeExists, result.ScopeExists)
				assert.ElementsMatch(t, tt.expected.OtherManagers, result.OtherManagers)
			}
		})
	}
}

func TestCheckOwnership_MigrationScenarios(t *testing.T) {
	t.Run("Migration scenario - from helm to eno", func(t *testing.T) {
		// Initial state: helm owns the initContainers
		fieldsV1Helm := map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:template": map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:initContainers": map[string]interface{}{},
					},
				},
			},
		}
		fieldsV1HelmBytes, _ := json.Marshal(fieldsV1Helm)

		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name": "test-deployment",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "init",
									"image": "busybox",
								},
							},
						},
					},
				},
			},
		}

		managedFields := []metav1.ManagedFieldsEntry{
			{
				Manager:   "helm",
				Operation: metav1.ManagedFieldsOperationApply,
				FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1HelmBytes},
			},
		}
		resource.SetManagedFields(managedFields)

		// Check ownership before migration
		status, err := CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.False(t, status.FullyOwnedByEno, "Should not be fully owned by eno before migration")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Contains(t, status.OtherManagers, "helm", "helm should be in other managers")

		// Simulate migration: eno now owns the field too
		fieldsV1Eno := map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:template": map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:initContainers": map[string]interface{}{},
					},
				},
			},
		}
		fieldsV1EnoBytes, _ := json.Marshal(fieldsV1Eno)

		managedFields = append(managedFields, metav1.ManagedFieldsEntry{
			Manager:   "eno",
			Operation: metav1.ManagedFieldsOperationApply,
			FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1EnoBytes},
		})
		resource.SetManagedFields(managedFields)

		// Check ownership during migration (both own)
		status, err = CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.False(t, status.FullyOwnedByEno, "Should not be fully owned during migration")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Contains(t, status.OtherManagers, "helm", "helm should still be in other managers")

		// Simulate after migration: only eno owns the field
		managedFields = []metav1.ManagedFieldsEntry{
			{
				Manager:   "eno",
				Operation: metav1.ManagedFieldsOperationApply,
				FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1EnoBytes},
			},
		}
		resource.SetManagedFields(managedFields)

		// Check ownership after migration
		status, err = CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.True(t, status.FullyOwnedByEno, "Should be fully owned by eno after migration")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Empty(t, status.OtherManagers, "No other managers should exist")
	})

	t.Run("No migration scenario - eno already owns", func(t *testing.T) {
		fieldsV1Eno := map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:template": map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:initContainers": map[string]interface{}{},
					},
				},
			},
		}
		fieldsV1EnoBytes, _ := json.Marshal(fieldsV1Eno)

		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name": "test-deployment",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "init",
									"image": "busybox",
								},
							},
						},
					},
				},
			},
		}

		managedFields := []metav1.ManagedFieldsEntry{
			{
				Manager:   "eno",
				Operation: metav1.ManagedFieldsOperationApply,
				FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1EnoBytes},
			},
		}
		resource.SetManagedFields(managedFields)

		// Check ownership - should already be fully owned
		status, err := CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.True(t, status.FullyOwnedByEno, "Should be fully owned by eno")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Empty(t, status.OtherManagers, "No other managers should exist")
	})

	t.Run("No migration scenario - scope does not exist", func(t *testing.T) {
		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name": "test-deployment",
				},
				"spec": map[string]interface{}{
					"replicas": 1,
				},
			},
		}

		// Check ownership - scope doesn't exist, no migration needed
		status, err := CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.True(t, status.FullyOwnedByEno, "Should be considered fully owned when scope doesn't exist")
		assert.False(t, status.ScopeExists, "Scope should not exist")
		assert.Empty(t, status.OtherManagers, "No other managers should exist")
	})

	t.Run("Update operation manager detected - cannot force migrate", func(t *testing.T) {
		fieldsV1Update := map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:template": map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:initContainers": map[string]interface{}{},
					},
				},
			},
		}
		fieldsV1UpdateBytes, _ := json.Marshal(fieldsV1Update)

		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name": "test-deployment",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "init",
									"image": "busybox",
								},
							},
						},
					},
				},
			},
		}

		managedFields := []metav1.ManagedFieldsEntry{
			{
				Manager:   "Go-http-client",
				Operation: metav1.ManagedFieldsOperationUpdate, // Note: Update, not Apply
				FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1UpdateBytes},
			},
		}
		resource.SetManagedFields(managedFields)

		// Check ownership - Update manager detected
		status, err := CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.False(t, status.FullyOwnedByEno, "Should not be fully owned when Update manager exists")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Empty(t, status.OtherManagers, "No Apply managers should exist")
		assert.Contains(t, status.OtherUpdateManagers, "Go-http-client", "Go-http-client should be in Update managers")
	})

	t.Run("Mixed Apply and Update managers", func(t *testing.T) {
		fieldsV1Apply := map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:template": map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:initContainers": map[string]interface{}{},
					},
				},
			},
		}
		fieldsV1ApplyBytes, _ := json.Marshal(fieldsV1Apply)

		fieldsV1Update := map[string]interface{}{
			"f:spec": map[string]interface{}{
				"f:template": map[string]interface{}{
					"f:spec": map[string]interface{}{
						"f:initContainers": map[string]interface{}{},
					},
				},
			},
		}
		fieldsV1UpdateBytes, _ := json.Marshal(fieldsV1Update)

		resource := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name": "test-deployment",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "init",
									"image": "busybox",
								},
							},
						},
					},
				},
			},
		}

		managedFields := []metav1.ManagedFieldsEntry{
			{
				Manager:   "helm",
				Operation: metav1.ManagedFieldsOperationApply,
				FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1ApplyBytes},
			},
			{
				Manager:   "Go-http-client",
				Operation: metav1.ManagedFieldsOperationUpdate,
				FieldsV1:  &metav1.FieldsV1{Raw: fieldsV1UpdateBytes},
			},
		}
		resource.SetManagedFields(managedFields)

		// Check ownership - both types detected
		status, err := CheckOwnership(resource, "spec.template.spec.initContainers", "eno")
		require.NoError(t, err)
		assert.False(t, status.FullyOwnedByEno, "Should not be fully owned")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Contains(t, status.OtherManagers, "helm", "helm should be in Apply managers")
		assert.Contains(t, status.OtherUpdateManagers, "Go-http-client", "Go-http-client should be in Update managers")
	})
}
