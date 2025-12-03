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

// TestMigrationFlow verifies the complete ownership migration flow logic
func TestMigrationFlow(t *testing.T) {
	t.Run("deployment with initContainers owned by helm should be migrated", func(t *testing.T) {
		// Create a Deployment owned by helm with initContainers
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

		deployment := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":      "test-deployment",
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"replicas": int64(3),
					"selector": map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"app": "test",
						},
					},
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": map[string]interface{}{
								"app": "test",
							},
						},
						"spec": map[string]interface{}{
							"containers": []interface{}{
								map[string]interface{}{
									"name":  "main",
									"image": "nginx:latest",
								},
							},
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "init",
									"image": "busybox:latest",
								},
							},
						},
					},
				},
			},
		}

		now := metav1.Now()
		managedFields := []metav1.ManagedFieldsEntry{
			{
				Manager:    "helm",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "apps/v1",
				Time:       &now,
				FieldsType: "FieldsV1",
				FieldsV1:   &metav1.FieldsV1{Raw: fieldsV1HelmBytes},
			},
		}
		deployment.SetManagedFields(managedFields)

		// Step 1: Check that Deployment should be migrated
		gvk := schema.GroupVersionKind{
			Group:   "apps",
			Version: "v1",
			Kind:    "Deployment",
		}
		assert.True(t, shouldMigrateOwnership(gvk), "Deployment should support migration")

		// Step 2: Get the migration scope
		scope := getMigrationScope(gvk)
		assert.Equal(t, "spec.template.spec.initContainers", scope, "Scope should be initContainers path")

		// Step 3: Check ownership before migration - should need migration
		status, err := CheckOwnership(deployment, scope, enoFieldManager)
		require.NoError(t, err)
		assert.False(t, status.FullyOwnedByEno, "Should not be fully owned by eno before migration")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Contains(t, status.OtherManagers, "helm", "helm should be in other managers")

		// Step 4: Verify the migration object structure is correct
		scopePath := []string{"spec", "template", "spec", "initContainers"}
		scopeContent, found, err := unstructured.NestedFieldCopy(deployment.Object, scopePath...)
		require.NoError(t, err)
		assert.True(t, found, "Scope content should be found")
		assert.NotNil(t, scopeContent, "Scope content should not be nil")

		// Verify building migration object works
		migrationObj := &unstructured.Unstructured{}
		migrationObj.SetGroupVersionKind(deployment.GroupVersionKind())
		migrationObj.SetName(deployment.GetName())
		migrationObj.SetNamespace(deployment.GetNamespace())
		err = unstructured.SetNestedField(migrationObj.Object, scopeContent, scopePath...)
		require.NoError(t, err)

		// Verify migration object only contains the scope
		assert.Equal(t, deployment.GetKind(), migrationObj.GetKind())
		assert.Equal(t, deployment.GetName(), migrationObj.GetName())
		assert.Equal(t, deployment.GetNamespace(), migrationObj.GetNamespace())

		// Migration object should only have the initContainers path, not replicas or containers
		_, found, _ = unstructured.NestedInt64(migrationObj.Object, "spec", "replicas")
		assert.False(t, found, "Migration object should not have replicas field")

		_, found, _ = unstructured.NestedSlice(migrationObj.Object, "spec", "template", "spec", "containers")
		assert.False(t, found, "Migration object should not have containers field")

		// But it should have initContainers
		initContainers, found, err := unstructured.NestedSlice(migrationObj.Object, "spec", "template", "spec", "initContainers")
		require.NoError(t, err)
		assert.True(t, found, "Migration object should have initContainers")
		assert.Len(t, initContainers, 1, "initContainers should have one entry")
	})

	t.Run("deployment already owned by eno should not need migration", func(t *testing.T) {
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

		deployment := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":      "test-deployment",
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"template": map[string]interface{}{
						"spec": map[string]interface{}{
							"initContainers": []interface{}{
								map[string]interface{}{
									"name":  "init",
									"image": "busybox:latest",
								},
							},
						},
					},
				},
			},
		}

		now := metav1.Now()
		managedFields := []metav1.ManagedFieldsEntry{
			{
				Manager:    "eno",
				Operation:  metav1.ManagedFieldsOperationApply,
				APIVersion: "apps/v1",
				Time:       &now,
				FieldsType: "FieldsV1",
				FieldsV1:   &metav1.FieldsV1{Raw: fieldsV1EnoBytes},
			},
		}
		deployment.SetManagedFields(managedFields)

		scope := "spec.template.spec.initContainers"

		// Check ownership - should already be owned
		status, err := CheckOwnership(deployment, scope, enoFieldManager)
		require.NoError(t, err)
		assert.True(t, status.FullyOwnedByEno, "Should already be fully owned by eno")
		assert.True(t, status.ScopeExists, "Scope should exist")
		assert.Empty(t, status.OtherManagers, "No other managers should exist")
	})

	t.Run("deployment without initContainers should not need migration", func(t *testing.T) {
		deployment := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":      "test-deployment",
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"replicas": int64(1),
				},
			},
		}

		scope := "spec.template.spec.initContainers"

		// Check ownership - scope doesn't exist
		status, err := CheckOwnership(deployment, scope, enoFieldManager)
		require.NoError(t, err)
		assert.True(t, status.FullyOwnedByEno, "Should be considered fully owned when scope doesn't exist")
		assert.False(t, status.ScopeExists, "Scope should not exist")
		assert.Empty(t, status.OtherManagers, "No other managers should exist")
	})
}
