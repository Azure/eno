package kclshim

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSynthesize(t *testing.T) {
	// Load test input from fixture file
	inputBytes, err := os.ReadFile("fixtures/example_input.json")
	if err != nil {
		t.Fatalf("Failed to read input file: %v", err)
	}
	var input map[string]interface{}
	if err := json.Unmarshal(inputBytes, &input); err != nil {
		t.Fatalf("Failed to unmarshal input JSON: %v", err)
	}

	tests := []struct {
		name        string
		workingDir  string
		expectErr   bool
		errContains []string
		expectCount int
	}{
		{
			name:        "successful synthesis",
			workingDir:  "fixtures/example_synthesizer",
			expectErr:   false,
			expectCount: 2,
		},
		{
			name:        "failed synthesis",
			workingDir:  "fixtures/bad_example_synthesizer",
			expectErr:   true,
			errContains: []string{"error updating dependencies", "No such file or directory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := Synthesize(tt.workingDir, input)

			// Failure path
			if tt.expectErr {
				if err == nil {
					t.Fatal("Expected error, got nil")
				}
				for _, substr := range tt.errContains {
					if !strings.Contains(err.Error(), substr) {
						t.Errorf("Expected error containing %q, got: %v", substr, err)
					}
				}
				if output != nil {
					t.Errorf("Expected nil output on error, got %d items", len(output))
				}
				return
			}

			// Success path
			if err != nil {
				t.Fatalf("Synthesize returned unexpected error: %v", err)
			}
			if output == nil {
				t.Fatalf("Expected non-nil output, got nil")
			}

			// Verify output item count
			if len(output) != tt.expectCount {
				t.Fatalf("Expected %d items, got %d", tt.expectCount, len(output))
			}

			// Item[0]: Deployment
			deployment, ok := output[0].(*unstructured.Unstructured)
			if !ok {
				t.Fatalf("Expected output[0] to be *unstructured.Unstructured, got %T", output[0])
			}

			// apiVersion
			if deployment.GetAPIVersion() != "apps/v1" {
				t.Errorf("Deployment: expected apiVersion 'apps/v1', got %q", deployment.GetAPIVersion())
			}

			// kind
			if deployment.GetKind() != "Deployment" {
				t.Errorf("Deployment: expected kind 'Deployment', got %q", deployment.GetKind())
			}

			// metadata.name
			if deployment.GetName() != "my-deployment" {
				t.Errorf("Deployment: expected name 'my-deployment', got %q", deployment.GetName())
			}

			// metadata.namespace
			if deployment.GetNamespace() != "default" {
				t.Errorf("Deployment: expected namespace 'default', got %q", deployment.GetNamespace())
			}

			// spec.replicas
			replicas, found, err := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
			if err != nil || !found {
				t.Fatalf("Deployment: failed to find spec.replicas: found=%v, err=%v", found, err)
			}
			if replicas != 3 {
				t.Errorf("Deployment: expected spec.replicas 3, got %d", replicas)
			}

			// spec.selector.matchLabels.app
			selectorApp, found, err := unstructured.NestedString(deployment.Object, "spec", "selector", "matchLabels", "app")
			if err != nil || !found {
				t.Fatalf("Deployment: failed to find spec.selector.matchLabels.app: found=%v, err=%v", found, err)
			}
			if selectorApp != "my-app" {
				t.Errorf("Deployment: expected spec.selector.matchLabels.app 'my-app', got %q", selectorApp)
			}

			// spec.template.metadata.labels.app
			templateLabelApp, found, err := unstructured.NestedString(deployment.Object, "spec", "template", "metadata", "labels", "app")
			if err != nil || !found {
				t.Fatalf("Deployment: failed to find spec.template.metadata.labels.app: found=%v, err=%v", found, err)
			}
			if templateLabelApp != "my-app" {
				t.Errorf("Deployment: expected spec.template.metadata.labels.app 'my-app', got %q", templateLabelApp)
			}

			// spec.template.spec.containers
			containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
			if err != nil || !found {
				t.Fatalf("Deployment: failed to find spec.template.spec.containers: found=%v, err=%v", found, err)
			}
			if len(containers) != 1 {
				t.Fatalf("Deployment: expected 1 container, got %d", len(containers))
			}

			// spec.template.spec.containers[0].image
			container, ok := containers[0].(map[string]interface{})
			if !ok {
				t.Fatalf("Deployment: expected containers[0] to be map[string]interface{}, got %T", containers[0])
			}
			image, ok := container["image"].(string)
			if !ok {
				t.Fatal("Deployment: containers[0].image is not a string")
			}
			if image != "mcr.microsoft.com/a/b/my-image:latest" {
				t.Errorf("Deployment: expected containers[0].image 'mcr.microsoft.com/a/b/my-image:latest', got %q", image)
			}

			// spec.template.spec.containers[0].name
			containerName, ok := container["name"].(string)
			if !ok {
				t.Fatal("Deployment: containers[0].name is not a string")
			}
			if containerName != "my-container" {
				t.Errorf("Deployment: expected containers[0].name 'my-container', got %q", containerName)
			}

			// Item[1]: ServiceAccount
			sa, ok := output[1].(*unstructured.Unstructured)
			if !ok {
				t.Fatalf("Expected output[1] to be *unstructured.Unstructured, got %T", output[1])
			}

			// apiVersion
			if sa.GetAPIVersion() != "v1" {
				t.Errorf("ServiceAccount: expected apiVersion 'v1', got %q", sa.GetAPIVersion())
			}

			// kind
			if sa.GetKind() != "ServiceAccount" {
				t.Errorf("ServiceAccount: expected kind 'ServiceAccount', got %q", sa.GetKind())
			}

			// metadata.name
			if sa.GetName() != "my-service-account" {
				t.Errorf("ServiceAccount: expected name 'my-service-account', got %q", sa.GetName())
			}

			// metadata.namespace
			if sa.GetNamespace() != "default" {
				t.Errorf("ServiceAccount: expected namespace 'default', got %q", sa.GetNamespace())
			}
		})
	}
}
