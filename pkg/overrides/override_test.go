package overrides_test

import (
	"testing"

	"github.com/Azure/eno/pkg/overrides"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.o.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
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
