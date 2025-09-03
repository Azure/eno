package sdk

import (
	"encoding/json"
	"fmt"

	intcel "github.com/Azure/eno/internal/cel"
	intmut "github.com/Azure/eno/internal/resource/mutation"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Override represents an object in the eno.azure.io/overrides array.
type Override struct {
	Path      string `json:"path"`
	Value     any    `json:"value"`
	Condition string `json:"condition"`
}

func (o *Override) parseCondition() (cel.Program, error) {
	if o.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	_, err := intmut.ParsePathExpr(o.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse path: %w", err)
	}

	if o.Condition == "" {
		return nil, fmt.Errorf("condition is required")
	}

	ast, issues := intcel.Env.Parse(o.Condition)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to parse condition: %w", issues.Err())
	}

	return intcel.Env.Program(ast)
}

// Test evaluates the override's condition against the provided CEL scope.
// Useful for unit testing condition logic.
func (o *Override) Test(data map[string]any) (bool, error) {
	prg, err := o.parseCondition()
	if err != nil {
		return false, fmt.Errorf("failed to validate override: %w", err)
	}

	result, _, err := prg.Eval(data)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate condition: %w", err)
	}

	if boolVal, ok := result.(types.Bool); ok {
		return bool(boolVal), nil
	}

	return false, fmt.Errorf("condition did not evaluate to boolean, got: %T", result)
}

// String returns a human readable representation of the override with minimal escaping for readability.
func (o *Override) String() string {
	return fmt.Sprintf("{Path: %s,\n Value: %v,\n Condition: %s}", o.Path, o.Value, o.Condition)
}

// AnnotateOverrides will take care of appropriatly serializng your overrides to annotations
// merging them with others that exist
func AnnotateOverrides(obj client.Object, overrides []Override) error {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	merged := overrides
	if existingStr, exists := annotations["eno.azure.io/overrides"]; exists {
		var existing []Override
		json.Unmarshal([]byte(existingStr), &existing)
		merged = append(merged, overrides...)
	}

	jsonBytes, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("failed to marshal overrides: %w", err)
	}

	annotations["eno.azure.io/overrides"] = string(jsonBytes)
	obj.SetAnnotations(annotations)
	return nil
}
