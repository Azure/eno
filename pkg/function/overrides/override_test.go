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
				Path:      "self.metadata.name",
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
				Path:      "self.metadata.name",
				Condition: "",
			},
			wantErr: true,
		},
		{
			name: "InvalidConditionSyntax",
			o: overrides.Override{
				Path:      "self.metadata.name",
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

func TestValueProgram(t *testing.T) {
	tests := []struct {
		name    string
		o       overrides.Override
		wantErr bool
	}{
		{
			name: "ValidValueProgram",
			o: overrides.Override{
				Path:         "self.metadata.name",
				Condition:    "true",
				ValueProgram: "self.metadata.name",
			},
			wantErr: false,
		},
		{
			name: "InvalidValueProgram",
			o: overrides.Override{
				Path:         "self.metadata.name",
				Condition:    "true",
				ValueProgram: "1 +",
			},
			wantErr: true,
		},
		{
			name: "ValueProgramWithoutValue",
			o: overrides.Override{
				Path:         "self.spec.foo",
				Condition:    "has(self.spec.foo)",
				ValueProgram: "self.spec.foo",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.o.Test(map[string]any{
				"self": map[string]any{
					"metadata": map[string]any{"name": "test"},
					"spec":     map[string]any{"foo": "bar"},
				},
			})
			if (err != nil) != tt.wantErr {
				t.Errorf("Test() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
func TestAnnotateOverrides_Table(t *testing.T) {
	tests := []struct {
		name               string
		existingAnnotation string
		overrides          []overrides.Override
		expectedCount      int
		expectedOverrides  []overrides.Override
	}{
		{
			name: "ValueProgram",
			overrides: []overrides.Override{{
				Path:         "self.data.foo",
				Condition:    "has(self.data.foo)",
				ValueProgram: "self.data.foo",
			}},
			expectedCount: 1,
			expectedOverrides: []overrides.Override{{
				Path:         "self.data.foo",
				Condition:    "has(self.data.foo)",
				ValueProgram: "self.data.foo",
			}},
		},
		{
			name: "MultipleOverrides",
			overrides: []overrides.Override{
				{Path: "metadata.name", Condition: "true"},
				{Path: "metadata.namespace", Condition: "false"},
			},
			expectedCount: 2,
			expectedOverrides: []overrides.Override{
				{Path: "metadata.name", Condition: "true"},
				{Path: "metadata.namespace", Condition: "false"},
			},
		},
		{
			name:               "ExistingAnnotation_MergesOverrides",
			existingAnnotation: `[{"path":"metadata.name","condition":"true"}]`,
			overrides: []overrides.Override{
				{Path: "metadata.namespace", Condition: "true"},
			},
			expectedCount: 2,
			expectedOverrides: []overrides.Override{
				{Path: "metadata.name", Condition: "true"},
				{Path: "metadata.namespace", Condition: "true"},
			},
		},
		{
			name: "InvalidOverride_StillSerializes",
			overrides: []overrides.Override{
				{Path: "", Condition: "true"},
			},
			expectedCount: 1,
			expectedOverrides: []overrides.Override{
				{Path: "", Condition: "true"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
				},
			}
			if tt.existingAnnotation != "" {
				obj.SetAnnotations(map[string]string{
					"eno.azure.io/overrides": tt.existingAnnotation,
				})
			}

			err := overrides.AnnotateOverrides(obj, tt.overrides)
			if err != nil {
				t.Fatalf("AnnotateOverrides() error: %v", err)
			}

			anns := obj.GetAnnotations()
			val, ok := anns["eno.azure.io/overrides"]
			if !ok {
				t.Fatalf("expected annotation to be set")
			}

			var got []overrides.Override
			if err := json.Unmarshal([]byte(val), &got); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if len(got) != tt.expectedCount {
				t.Fatalf("expected %d overrides, got %d", tt.expectedCount, len(got))
			}

			for i, expected := range tt.expectedOverrides {
				if got[i].Path != expected.Path {
					t.Errorf("override[%d].Path = %q, want %q", i, got[i].Path, expected.Path)
				}
				if got[i].Condition != expected.Condition {
					t.Errorf("override[%d].Condition = %q, want %q", i, got[i].Condition, expected.Condition)
				}
				if got[i].ValueProgram != expected.ValueProgram {
					t.Errorf("override[%d].ValueProgram = %q, want %q", i, got[i].ValueProgram, expected.ValueProgram)
				}
			}
		})
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
		triggered   bool
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
			triggered:   true,
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
			triggered:   false,
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
			triggered:   true,
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
			triggered:   false,
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
			triggered:   true,
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
			triggered:   false,
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
			triggered:   false,
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
			triggered:   false,
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
			if got != tt.triggered {
				t.Errorf("ReplaceIf() = %v, want %v", got, tt.triggered)
			}
		})
	}
}

func TestAllowVPA(t *testing.T) {
	vpaOverrides, err := overrides.AllowVPA("retina", v1.ResourceRequirements{
		Requests: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("100m"),
			v1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("500m"),
			v1.ResourceMemory: resource.MustParse("512Mi"),
		},
	})
	if err != nil {
		t.Fatalf("AllowVPA() error = %v", err)
	}
	if len(vpaOverrides) != 4 {
		t.Fatalf("AllowVPA() triggered 4 overrides, got %d", len(vpaOverrides))
	}

	tests := []struct {
		name        string
		condition   string
		data        map[string]any
		triggered   bool
		expectError bool
	}{
		{
			name: "don't trigger cpu requests when less",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"requests": map[string]any{
												"cpu": "90m",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			triggered:   false,
			expectError: false,
		},
		{
			name: "trigger when cpu requests when higher",
			data: map[string]any{
				"pathManagedByEno": false,
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
			triggered:   true,
			expectError: false,
		},
		{
			name: "don't trigger  when managed by eno",
			data: map[string]any{
				"pathManagedByEno": true,
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
			triggered:   false,
			expectError: false,
		},
		{
			name: "don't trigger memory requests when less",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"requests": map[string]any{
												"memory": "64Mi",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			triggered:   false,
			expectError: false,
		},
		{
			name: "replace memory requests when higher",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"requests": map[string]any{
												"memory": "256Mi",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			triggered:   true,
			expectError: false,
		},
		{
			name: "don't trigger for cpu limits when less",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"limits": map[string]any{
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
			triggered:   false,
			expectError: false,
		},
		{
			name: "trigger cpu limits when higher",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"limits": map[string]any{
												"cpu": "1000m",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			triggered:   true,
			expectError: false,
		},
		{
			name: "don't trigger for memory limits when less",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"limits": map[string]any{
												"memory": "256Mi",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			triggered:   false,
			expectError: false,
		},
		{
			name: "trigger for memory limits when higher",
			data: map[string]any{
				"pathManagedByEno": false,
				"self": map[string]any{
					"spec": map[string]any{
						"template": map[string]any{
							"spec": map[string]any{
								"containers": []map[string]any{
									{
										"name": "retina",
										"resources": map[string]any{
											"limits": map[string]any{
												"memory": "1Gi",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			triggered:   true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anytriggered := false
			for _, ov := range vpaOverrides {
				got, err := ov.Test(tt.data)
				if (err != nil) != tt.expectError {
					t.Errorf("AllowVPA() error = %v, expectError %v", err, tt.expectError)
					return
				}
				anytriggered = got || anytriggered
			}
			if anytriggered != tt.triggered {
				t.Errorf("anytriggered= %v, want %v", anytriggered, tt.triggered)
			}

		})
	}

}
