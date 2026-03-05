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

	t.Run("successful synthesis", func(t *testing.T) {
		output, err := Synthesize("fixtures/example_synthesizer", input)
		if err != nil {
			t.Fatalf("Synthesize returned unexpected error: %v", err)
		}
		if output == nil {
			t.Fatalf("Expected non-nil output, got nil")
		}

		// Verify the output []client.Object has the expected items
		if len(output) != 2 {
			t.Fatalf("Expected 2 items, got %d", len(output))
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
			t.Fatalf("Expected containers[0] to be map[string]interface{}, got %T", containers[0])
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

	t.Run("failed synthesis", func(t *testing.T) {
		output, err := Synthesize("fixtures/bad_example_synthesizer", input)

		// Verify error is returned
		if err == nil {
			t.Fatal("Expected error from bad synthesizer, got nil")
		}

		// Verify output is nil (no items produced)
		if output != nil {
			t.Errorf("Expected nil output on error, got %d items", len(output))
		}

		// Verify error message
		if !strings.Contains(err.Error(), "error updating dependencies") {
			t.Errorf("Expected error containing 'error updating dependencies', got: %v", err)
		}
		if !strings.Contains(err.Error(), "No such file or directory") {
			t.Errorf("Expected error containing 'No such file or directory', got: %v", err)
		}
	})
}
