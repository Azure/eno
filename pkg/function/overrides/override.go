package overrides

import (
	"encoding/json"
	"fmt"

	intcel "github.com/Azure/eno/internal/cel"
	intmut "github.com/Azure/eno/internal/resource/mutation"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// mirror of type Op struct  and type jsonOp struct  in internal/resource/mutation/mutation.go
// could pull those out
type Override struct {
	Path      string `json:"path"`
	Value     any    `json:"value"`
	Condition string `json:"condition"`
}

func (o *Override) validate() (cel.Program, error) {

	if o.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	//Not taking a dependency
	_, err := intmut.ParsePathExpr(o.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse path: %w", err)
	}

	if o.Condition == "" {
		return nil, fmt.Errorf("condition is required")
	}
	// Parse the expression
	celEnv := intcel.Env
	ast, issues := celEnv.Parse(o.Condition)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to parse condition: %w", issues.Err())
	}

	// Type-check the expression
	checked, issues := celEnv.Check(ast)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to type-check condition: %w", issues.Err())
	}

	// Create the program
	p, err := celEnv.Program(checked)
	if err != nil {
		return nil, fmt.Errorf("failed to create program: %w", err)
	}

	//Value can be null which is abit wierd.
	return p, nil

}

func (o *Override) Test(data map[string]interface{}) (bool, error) {
	// Evaluate with the input data

	prg, err := o.validate()
	if err != nil {
		return false, fmt.Errorf("failed to validate override: %w", err)
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

// String is for debugging only because escaped json cel is hard to read.
func (o *Override) String() string {
	//not actual json becuse escaping is hard to read.
	return fmt.Sprintf("{Path: %s,\n Value: %v,\n Condition: %s}", o.Path, o.Value, o.Condition)
}

// AnnotateOverrides will take care of appropriatly serializng your overrides to annotations
// merging them with others that exist
func AnnotateOverrides(obj *unstructured.Unstructured, overrides []Override) error {

	// Add Helm annotations that are required for Helm to recognize the resources
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

	for _, override := range merged {
		if _, err := override.validate(); err != nil {
			return fmt.Errorf("validating override: %w", err)
		}
	}

	jsonBytes, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("failed to marshal overrides: %w", err)
	}

	//should we append to existing annoations or panic if they exist?
	annotations["eno.azure.io/overrides"] = string(jsonBytes)

	obj.SetAnnotations(annotations)
	return nil
}

// ReplaceIf uses overrides to create a conditonal eno.azure.io/replace that only applies when some condition is met
// useful if you want server side apply most of the time except for some corner cases
func ReplaceIf(condition string) (Override, error) {
	true := "true"
	o := Override{
		Path:      `self.metadata.annotations["eno.azure.io/replace"]`,
		Value:     &true,
		Condition: condition,
	}
	//even if they didn't test ensure it valdiates
	if _, err := o.validate(); err != nil {
		return Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}

// AllowVPA lets VPA or external actor raise resources/requests for a given container (need to do limit too)
func AllowVPA(container string, req corev1.ResourceRequirements) ([]Override, error) {
	overrides := []Override{}
	for rtype, value := range req.Requests {
		if value.IsZero() {
			continue // skip zero values
		}
		o, err := allowVPA(container, rtype.String(), "requests", value.String())
		if err != nil {
			return nil, fmt.Errorf("creating override for requests: %w", err)
		}
		overrides = append(overrides, o)
	}
	for rtype, value := range req.Limits {
		if value.IsZero() {
			continue // skip zero values
		}
		o, err := allowVPA(container, rtype.String(), "limits", value.String())
		if err != nil {
			return nil, fmt.Errorf("creating override for limits: %w", err)
		}
		overrides = append(overrides, o)
	}
	return overrides, nil
}

func allowVPA(container, resourceType, reqOrLimits, value string) (Override, error) {

	path := fmt.Sprintf("self.spec.template.spec.containers[name='%s'].resources.%s.%s", container, reqOrLimits, resourceType)

	//"self.spec.template.spec.containers.exists(c, c.name == '%s' &&  has(c.resources.requests) &&  '%s' in c.resources.requests &&  compareResourceQuantities(c.resources.requests['%s'], '%s') > 0)"
	// self.spec.template.spec.containers.exists(c, c.name == 'retina' && has(c.resources.requests) && 'cpu' in c.resources.requests &&  compareResourceQuantities(c.resources.requests['cpu'], '100') > 0)}
	//to get && !pathManagedByEno to work need to pass in a  field manager to Test
	// also changed >= 0 to > 0
	// this is pretty unreadable use go text templating instead?
	cel := `self.spec.template.spec.containers.exists(c, c.name == '%s' && has(c.resources.%s) && '%s' in c.resources.%s && compareResourceQuantities(c.resources.%s['%s'], '%s') > 0)`
	condition := fmt.Sprintf(cel, container, reqOrLimits, resourceType, reqOrLimits, reqOrLimits, resourceType, value)
	o := Override{
		Path:      path,
		Value:     nil,
		Condition: condition,
	}
	if _, err := o.validate(); err != nil {
		return Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}
