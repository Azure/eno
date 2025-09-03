package stdlib

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAllowVPA(t *testing.T) {
	vpaOverrides, err := AllowVPA("retina", corev1.ResourceRequirements{
		Requests: map[corev1.ResourceName]resource.Quantity{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Limits: map[corev1.ResourceName]resource.Quantity{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
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

func TestReplaceIf(t *testing.T) {
	isovalent, err := ReplaceIf(`has(self.metadata.labels) && has(self.metadata.labels.billing) && self.metadata.labels.billing.startsWith("Isovalent")`)
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
