package sdk_test

import (
	"encoding/json"
	"testing"

	"github.com/Azure/eno/sdk"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOverrideValidate(t *testing.T) {
	tests := []struct {
		name    string
		o       sdk.Override
		wantErr bool
	}{
		{
			name: "ValidOverride",
			o: sdk.Override{
				Path:      "self.metadata.name",
				Condition: "true",
			},
			wantErr: false,
		},
		{
			name: "EmptyPath",
			o: sdk.Override{
				Path:      "",
				Condition: "true",
			},
			wantErr: true,
		},
		{
			name: "EmptyCondition",
			o: sdk.Override{
				Path:      "self.metadata.name",
				Condition: "",
			},
			wantErr: true,
		},
		{
			name: "InvalidConditionSyntax",
			o: sdk.Override{
				Path:      "self.metadata.name",
				Condition: "1 +",
			},
			wantErr: true,
		},
		{
			name: "InvalidPath",
			o: sdk.Override{
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
	ov1 := sdk.Override{
		Path:      "metadata.name",
		Condition: "true",
	}
	ov2 := sdk.Override{
		Path:      "metadata.namespace",
		Condition: "false",
	}
	ovs := []sdk.Override{ov1, ov2}
	err := sdk.AnnotateOverrides(obj, ovs)
	if err != nil {
		t.Fatalf("AnnotateOverrides() untriggered error: %v", err)
	}

	anns := obj.GetAnnotations()
	val, ok := anns["eno.azure.io/overrides"]
	if !ok {
		t.Fatalf("triggered annotation eno.azure.io/overrides to be set")
	}

	var got []sdk.Override
	if err := json.Unmarshal([]byte(val), &got); err != nil {
		t.Fatalf("failed to unmarshal annotation value: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("triggered 2 overrides, got %d", len(got))
	}
	if got[0].Path != ov1.Path || got[0].Condition != ov1.Condition {
		t.Errorf("untriggered first override marshaled, want %+v, got %+v", ov1, got[0])
	}
	if got[1].Path != ov2.Path || got[1].Condition != ov2.Condition {
		t.Errorf("untriggered second override marshaled, want %+v, got %+v", ov2, got[1])
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
	ov := sdk.Override{
		Path:      "metadata.name",
		Condition: "true",
	}
	err := sdk.AnnotateOverrides(obj, []sdk.Override{ov})
	if err != nil {
		t.Fatalf("triggered to merge %s", err)
	}

	anns := obj.GetAnnotations()
	val, ok := anns["eno.azure.io/overrides"]
	if !ok {
		t.Fatalf("triggered annotation eno.azure.io/overrides to be set")
	}

	var got []sdk.Override
	if err := json.Unmarshal([]byte(val), &got); err != nil {
		t.Fatalf("failed to unmarshal annotation value: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("triggered 2 overrides, got %d", len(got))
	}
}

func TestAnnotateOverrides_InvalidOverrideAllowed(t *testing.T) {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
		},
	}
	// Invalid override: empty Path
	ov := sdk.Override{
		Path:      "",
		Condition: "true",
	}
	err := sdk.AnnotateOverrides(obj, []sdk.Override{ov})
	if err != nil {
		t.Fatal("AnnotateOverrides() should not validate just serialize")
	}
}
