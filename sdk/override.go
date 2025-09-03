package sdk

import (
	"encoding/json"
	"fmt"

	intcel "github.com/Azure/eno/internal/cel"
	intmut "github.com/Azure/eno/internal/resource/mutation"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	corev1 "k8s.io/api/core/v1"
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
	if _, err := o.parseCondition(); err != nil {
		return Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}

// AllowVPA lets VPA or external actor raise resources/requests for a given container. It checks if the requests and limits
// are higher and also that the path is not managed by eno (so eno can lower if eno was the last updater)
func AllowVPA(container string, req corev1.ResourceRequirements) ([]Override, error) {
	overrides := []Override{}
	requirementsMap := map[string]corev1.ResourceList{
		"requests": req.Requests,
		"limits":   req.Limits,
	}

	for name, resourceList := range requirementsMap {
		for rtype, value := range resourceList {
			if value.IsZero() {
				continue // skip zero values
			}
			o, err := allowVPA(container, rtype.String(), name, value.String())
			if err != nil {
				return nil, fmt.Errorf("creating override for %s: %w", name, err)
			}
			overrides = append(overrides, o)
		}
	}

	return overrides, nil
}

func allowVPA(container, resourceType, reqOrLimits, value string) (Override, error) {

	path := fmt.Sprintf("self.spec.template.spec.containers[name='%s'].resources.%s.%s", container, reqOrLimits, resourceType)

	// this is pretty unreadable use go text templating instead?
	// it basically says if its not managed by us and >= some minumum then use nuil to not mess with it.
	cel := `!pathManagedByEno && self.spec.template.spec.containers.exists(c, c.name == '%s' && has(c.resources.%s) && '%s' in c.resources.%s && compareResourceQuantities(c.resources.%s['%s'], '%s') >= 0)`
	condition := fmt.Sprintf(cel, container, reqOrLimits, resourceType, reqOrLimits, reqOrLimits, resourceType, value)
	o := Override{
		Path:      path,
		Value:     nil,
		Condition: condition,
	}
	if _, err := o.parseCondition(); err != nil {
		return Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}
