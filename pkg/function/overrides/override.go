package overrides

import (
	"encoding/json"
	"fmt"

	intcel "github.com/Azure/eno/internal/cel"
	"github.com/google/cel-go/common/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mirror of type Op struct  and type jsonOp struct  in internal/resource/mutation/mutation.go
// could pull those out
type Override struct {
	Path      string  `json:"path"`
	Value     *string `json:"value"`
	Condition string  `json:"condition"`
}

func (o *Override) Validate() error {
	if o.Path == "" {
		return fmt.Errorf("path is required")
	}

	if o.Condition == "" {
		return fmt.Errorf("condition is required")
	}
	celEnv := intcel.Env
	// Parse the expression
	ast, issues := celEnv.Parse(o.Condition)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("failed to parse condition: %w", issues.Err())
	}

	// Type-check the expression
	checked, issues := celEnv.Check(ast)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("failed to type-check condition: %w", issues.Err())
	}

	// Create the program
	_, err := celEnv.Program(checked)
	if err != nil {
		return fmt.Errorf("failed to create program: %w", err)
	}

	//Value can be null which is abit wierd.
	return nil
}

func (o *Override) Test(data map[string]interface{}) (bool, error) {
	// Evaluate with the input data

	celEnv := intcel.Env
	// Parse the expression
	ast, issues := celEnv.Parse(o.Condition)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("failed to parse condition: %w", issues.Err())
	}

	// Type-check the expression
	checked, issues := celEnv.Check(ast)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Errorf("failed to type-check condition: %w", issues.Err())
	}

	// Create the program
	prg, err := celEnv.Program(checked)
	if err != nil {
		return false, fmt.Errorf("failed to create program: %w", err)
	}

	result, _, err := prg.Eval(data)
	if err != nil {
		return false, fmt.Errorf("failed to evaluate condition: %w", err)
	}

	// Convert result to boolean
	if boolVal, ok := result.(types.Bool); ok {
		return bool(boolVal), nil
	}

	return false, fmt.Errorf("condition did not evaluate to boolean, got: %T", result)
}

func AnnotateOverrides(obj *unstructured.Unstructured, overrides []Override) error {
	for _, override := range overrides {
		if err := override.Validate(); err != nil {
			return fmt.Errorf("validating override: %w", err)
		}
	}

	// Add Helm annotations that are required for Helm to recognize the resources
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	jsonBytes, err := json.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("failed to marshal overrides: %w", err)
	}

	if _, exists := annotations["eno.azure.io/overrides"]; exists {
		return fmt.Errorf("annotation eno.azure.io/overrides already exists, cannot overwrite")
	}

	//should we append to existing annoations or panic if they exist?
	annotations["eno.azure.io/overrides"] = string(jsonBytes)

	obj.SetAnnotations(annotations)
	return nil
}

func ReplaceIf(condition string) (Override, error) {
	true := "true"
	o := Override{
		Path:      `self.metadata.annotations["eno.azure.io/replace"]`,
		Value:     &true,
		Condition: condition,
	}
	if err := o.Validate(); err != nil {
		return Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}

// Let VPA or external actor raise resources/requests for a given container (need to do limit too)
// USe https://pkg.go.dev/golang.org/x/tools/cmd/stringer to pass enums instead of strings
func AllowVPA(container, value, rtype string) (Override, error) {
	if rtype != "cpu" && rtype != "memory" {
		return Override{}, fmt.Errorf("invalid type %s, must be 'cpu' or 'memory'", rtype)
	}
	path := fmt.Sprintf("self.spec.template.spec.containers[name='%s'].resources.requests.%s", container, rtype)

	//to get && !pathManagedByEno to work need to passs ina  field manager to Test
	// also changed >= 0 to > 0
	condition := fmt.Sprintf("self.spec.template.spec.containers.exists(c, c.name == '%s' &&  has(c.resources.requests) &&  '%s' in c.resources.requests &&  compareResourceQuantities(c.resources.requests['%s'], '%s') > 0)", container, rtype, rtype, value)
	o := Override{
		Path:      path,
		Value:     nil,
		Condition: condition,
	}
	if err := o.Validate(); err != nil {
		return Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}
