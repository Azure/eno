package reconciliation

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRemoveScopeFromManagedFields_UpdateManager(t *testing.T) {
	// Create a deployment with Go-http-client as Update manager owning initContainers
	deployment := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "test-deployment",
				"namespace": "default",
				"managedFields": []interface{}{
					map[string]interface{}{
						"manager":    "Go-http-client",
						"operation":  "Update",
						"apiVersion": "apps/v1",
						"time":       "2025-12-02T17:51:19Z",
						"fieldsType": "FieldsV1",
						"fieldsV1": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:template": map[string]interface{}{
									"f:spec": map[string]interface{}{
										"f:initContainers": map[string]interface{}{
											".": map[string]interface{}{},
											"k:{\"name\":\"base-os-bash\"}": map[string]interface{}{
												".":                 map[string]interface{}{},
												"f:command":         map[string]interface{}{},
												"f:image":           map[string]interface{}{},
												"f:imagePullPolicy": map[string]interface{}{},
												"f:name":            map[string]interface{}{},
											},
										},
									},
								},
							},
						},
					},
					map[string]interface{}{
						"manager":    "eno",
						"operation":  "Apply",
						"apiVersion": "apps/v1",
						"time":       "2025-12-02T18:00:00Z",
						"fieldsType": "FieldsV1",
						"fieldsV1": map[string]interface{}{
							"f:spec": map[string]interface{}{
								"f:replicas": map[string]interface{}{},
							},
						},
					},
				},
			},
			"spec": map[string]interface{}{
				"replicas": 1,
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"initContainers": []interface{}{
							map[string]interface{}{
								"name":            "base-os-bash",
								"image":           "bash:latest",
								"command":         []interface{}{"/bin/bash"},
								"imagePullPolicy": "Always",
							},
						},
					},
				},
			},
		},
	}

	// Convert managedFields from map to proper type
	managedFields := []metav1.ManagedFieldsEntry{
		{
			Manager:    "Go-http-client",
			Operation:  metav1.ManagedFieldsOperationUpdate,
			APIVersion: "apps/v1",
			Time:       &metav1.Time{},
			FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{
				Raw: []byte(`{"f:spec":{"f:template":{"f:spec":{"f:initContainers":{".":{},"k:{\"name\":\"base-os-bash\"}":{".":{},"f:command":{},"f:image":{},"f:imagePullPolicy":{},"f:name":{}}}}}}}`),
			},
		},
		{
			Manager:    "eno",
			Operation:  metav1.ManagedFieldsOperationApply,
			APIVersion: "apps/v1",
			Time:       &metav1.Time{},
			FieldsType: "FieldsV1",
			FieldsV1: &metav1.FieldsV1{
				Raw: []byte(`{"f:spec":{"f:replicas":{}}}`),
			},
		},
	}
	deployment.SetManagedFields(managedFields)

	// Test removing initContainers scope from Go-http-client
	modified, err := removeScopeFromManagedFields(deployment, "spec.template.spec.initContainers", []string{"Go-http-client"})
	if err != nil {
		t.Fatalf("removeScopeFromManagedFields failed: %v", err)
	}

	if !modified {
		t.Fatal("Expected managedFields to be modified")
	}

	// Verify Go-http-client no longer has initContainers in its managed fields
	updatedManagedFields := deployment.GetManagedFields()
	for _, entry := range updatedManagedFields {
		if entry.Manager == "Go-http-client" {
			// The entry should either be removed or no longer contain initContainers
			status, err := CheckOwnership(deployment, "spec.template.spec.initContainers", "eno")
			if err != nil {
				t.Fatalf("CheckOwnership failed: %v", err)
			}

			// Go-http-client should no longer be in the update managers list
			found := false
			for _, mgr := range status.OtherUpdateManagers {
				if mgr == "Go-http-client" {
					found = true
					break
				}
			}
			if found {
				t.Errorf("Go-http-client should not be in OtherUpdateManagers after removal")
			}
		}
	}

	t.Logf("Successfully removed initContainers scope from Go-http-client's managedFields")
}
