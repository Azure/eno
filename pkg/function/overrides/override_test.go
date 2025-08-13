package overrides_test

import (
	"encoding/json"
	"testing"

	"github.com/Azure/eno/pkg/function/overrides"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOverrideValidate(t *testing.T) {
	tests := []struct {
		name    string
		o       overrides.Override
		wantErr bool
	}{
		{
			name: "ValidOverride",
			o: overrides.Override{
				Path:      "metadata.name",
				Condition: "true",
			},
			wantErr: false,
		},
		{
			name: "EmptyPath",
			o: overrides.Override{
				Path:      "",
				Condition: "true",
			},
			wantErr: true,
		},
		{
			name: "EmptyCondition",
			o: overrides.Override{
				Path:      "metadata.name",
				Condition: "",
			},
			wantErr: true,
		},
		{
			name: "InvalidConditionSyntax",
			o: overrides.Override{
				Path:      "metadata.name",
				Condition: "1 +",
			},
			wantErr: true,
		},
		{
			name: "InvalidPath",
			o: overrides.Override{
				Path:      "I <3 Candy",
				Condition: "true",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.o.Test(map[string]any{})
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAnnotateOverrides_Success(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
		},
	}
	ov1 := overrides.Override{
		Path:      "metadata.name",
		Condition: "true",
	}
	ov2 := overrides.Override{
		Path:      "metadata.namespace",
		Condition: "false",
	}
	ovs := []overrides.Override{ov1, ov2}
	err := overrides.AnnotateOverrides(obj, ovs)
	if err != nil {
		t.Fatalf("AnnotateOverrides() unexpected error: %v", err)
	}

	anns := obj.GetAnnotations()
	val, ok := anns["eno.azure.io/overrides"]
	if !ok {
		t.Fatalf("expected annotation eno.azure.io/overrides to be set")
	}

	var got []overrides.Override
	if err := json.Unmarshal([]byte(val), &got); err != nil {
		t.Fatalf("failed to unmarshal annotation value: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 overrides, got %d", len(got))
	}
	if got[0].Path != ov1.Path || got[0].Condition != ov1.Condition {
		t.Errorf("unexpected first override marshaled, want %+v, got %+v", ov1, got[0])
	}
	if got[1].Path != ov2.Path || got[1].Condition != ov2.Condition {
		t.Errorf("unexpected second override marshaled, want %+v, got %+v", ov2, got[1])
	}
}

func TestAnnotateOverrides_ExistingAnnotation(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
		},
	}
	// Pre-set the annotation to simulate duplicate
	obj.SetAnnotations(map[string]string{
		"eno.azure.io/overrides": "[{\"path\":\"metadata.name2\",\"condition\":\"true\"}]",
	})
	ov := overrides.Override{
		Path:      "metadata.name",
		Condition: "true",
	}
	err := overrides.AnnotateOverrides(obj, []overrides.Override{ov})
	if err != nil {
		t.Fatalf("expected to merge %s", err)
	}

	anns := obj.GetAnnotations()
	val, ok := anns["eno.azure.io/overrides"]
	if !ok {
		t.Fatalf("expected annotation eno.azure.io/overrides to be set")
	}

	var got []overrides.Override
	if err := json.Unmarshal([]byte(val), &got); err != nil {
		t.Fatalf("failed to unmarshal annotation value: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 overrides, got %d", len(got))
	}
}

func TestAnnotateOverrides_InvalidOverride(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
		},
	}
	// Invalid override: empty Path
	ov := overrides.Override{
		Path:      "",
		Condition: "true",
	}
	err := overrides.AnnotateOverrides(obj, []overrides.Override{ov})
	if err == nil {
		t.Fatal("AnnotateOverrides() expected validation error for invalid override, got nil")
	}
}

func TestReplaceIf(t *testing.T) {
	isovalent, err := overrides.ReplaceIf(`has(self.metadata.labels) && has(self.metadata.labels.billing) && self.metadata.labels.billing.startsWith("Isovalent")`)
	if err != nil {
		t.Fatalf("ReplaceIf() error = %v", err)
	}

	tests := []struct {
		name        string
		condition   string
		data        map[string]any
		expected    bool
		expectError bool
	}{
		{
			name: "simple null check - true case",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{
							"billing": "Isovalent-Enterprise",
						},
					},
				},
			},
			expected:    true,
			expectError: false,
		},
		{
			name: "simple null check - false case",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": nil,
					},
				},
			},
			expected:    false,
			expectError: false,
		},
		{
			name: "string startsWith - true case",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{
							"billing": "Isovalent-Enterprise",
						},
					},
				},
			},
			expected:    true,
			expectError: false,
		},
		{
			name: "string startsWith - false case",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{
							"billing": "Standard",
						},
					},
				},
			},
			expected:    false,
			expectError: false,
		},
		{
			name: "complex condition - true case",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{
							"billing": "Isovalent-Enterprise",
						},
					},
				},
			},
			expected:    true,
			expectError: false,
		},
		{
			name: "complex condition - false case (null labels)",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": nil,
					},
				},
			},
			expected:    false,
			expectError: false,
		},
		{
			name: "billing label not present",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{
							"totallyfree": "Isovalent-Enterprise",
						},
					},
				},
			},
			expected:    false,
			expectError: false,
		},
		{
			name: "integer label value - should fail without string conversion",
			data: map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{
						"labels": map[string]any{
							"billing": 12345, // integer instead of string
						},
					},
				},
			},
			expected:    false,
			expectError: true, // This should fail because int doesn't have startsWith method
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isovalent.Test(tt.data)
			if (err != nil) != tt.expectError {
				t.Errorf("ReplaceIf() error = %v, expectError %v", err, tt.expectError)
				return
			}
			if got != tt.expected {
				t.Errorf("ReplaceIf() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestAllowVPA(t *testing.T) {
	vpaOverrides, err := overrides.AllowVPA("retina", v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU: *resource.NewQuantity(100, resource.DecimalSI),
		},
	})
	if err != nil {
		t.Fatalf("AllowVPA() error = %v", err)
	}

	tests := []struct {
		name        string
		condition   string
		data        map[string]any
		expected    bool
		expectError bool
	}{
		{
			name: "don't replace its the same",
			data: map[string]any{
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"requests": map[string]any{
												"cpu": "100m",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expected:    false,
			expectError: false,
		},
		{
			name: "replace with null when higher",
			data: map[string]any{
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"requests": map[string]any{
												"cpu": "500m",
											},
										},
									},
								},
							},
						},
					},
				},
			},

			expected:    true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, ov := range vpaOverrides {
				got, err := ov.Test(tt.data)
				if (err != nil) != tt.expectError {
					t.Errorf("AllowVPA() error = %v, expectError %v", err, tt.expectError)
					return
				}
				if got != tt.expected {
					t.Errorf("AllowVPA() = %v, want %v", got, tt.expected)
				}
			}
		})
	}

}
