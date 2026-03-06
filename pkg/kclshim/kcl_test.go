package kclshim

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
		name           string
		workingDir     string
		expectedErrs   []string
		expectedOutput string
	}{
		{
			name:       "successful synthesis",
			workingDir: "fixtures/example_synthesizer",
			expectedOutput: `[
				{
					"apiVersion": "apps/v1",
					"kind": "Deployment",
					"metadata": {
						"name": "my-deployment",
						"namespace": "default"
					},
					"spec": {
						"replicas": 3,
						"selector": {
							"matchLabels": {
								"app": "my-app"
							}
						},
						"template": {
							"metadata": {
								"labels": {
									"app": "my-app"
								}
							},
							"spec": {
								"containers": [
									{
										"image": "mcr.microsoft.com/a/b/my-image:latest",
										"name": "my-container"
									}
								]
							}
						}
					}
				},
				{
					"apiVersion": "v1",
					"kind": "ServiceAccount",
					"metadata": {
						"name": "my-service-account",
						"namespace": "default"
					}
				}
			]`,
		},
		{
			name:         "failed synthesis",
			workingDir:   "fixtures/bad_example_synthesizer",
			expectedErrs: []string{"error updating dependencies", "No such file or directory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := Synthesize(tt.workingDir, input)

			// Failure path
			if len(tt.expectedErrs) > 0 {
				if err == nil {
					t.Fatal("Expected error, got nil")
				}
				for _, substr := range tt.expectedErrs {
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

			outputJSON, err := json.Marshal(output)
			if err != nil {
				t.Fatalf("Failed to marshal output to JSON: %v", err)
			}

			if normalizeWhitespace(string(outputJSON)) != normalizeWhitespace(tt.expectedOutput) {
				t.Errorf("Output mismatch.\nExpected:\n%s\nGot:\n%s", tt.expectedOutput, string(outputJSON))
			}
		})
	}
}

func normalizeWhitespace(s string) string {
	for _, whitespace := range []string{"\n", "\t", " "} {
		s = strings.ReplaceAll(s, whitespace, "")
	}
	return s
}
