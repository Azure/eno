package stdlib

import (
	"fmt"

	"github.com/Azure/eno/sdk"
	corev1 "k8s.io/api/core/v1"
)

// ReplaceIf uses overrides to create a conditonal eno.azure.io/replace that only applies when some condition is met
// useful if you want server side apply most of the time except for some corner cases
func ReplaceIf(condition string) (sdk.Override, error) {
	true := "true"
	o := sdk.Override{
		Path:      `self.metadata.annotations["eno.azure.io/replace"]`,
		Value:     &true,
		Condition: condition,
	}
	//even if they didn't test ensure it valdiates
	if _, err := o.parseCondition(); err != nil {
		return sdk.Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}

// AllowVPA lets VPA or external actor raise resources/requests for a given container. It checks if the requests and limits
// are higher and also that the path is not managed by eno (so eno can lower if eno was the last updater)
func AllowVPA(container string, req corev1.ResourceRequirements) ([]sdk.Override, error) {
	overrides := []sdk.Override{}
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

func allowVPA(container, resourceType, reqOrLimits, value string) (sdk.Override, error) {

	path := fmt.Sprintf("self.spec.template.spec.containers[name='%s'].resources.%s.%s", container, reqOrLimits, resourceType)

	// this is pretty unreadable use go text templating instead?
	// it basically says if its not managed by us and >= some minumum then use nuil to not mess with it.
	cel := `!pathManagedByEno && self.spec.template.spec.containers.exists(c, c.name == '%s' && has(c.resources.%s) && '%s' in c.resources.%s && compareResourceQuantities(c.resources.%s['%s'], '%s') >= 0)`
	condition := fmt.Sprintf(cel, container, reqOrLimits, resourceType, reqOrLimits, reqOrLimits, resourceType, value)
	o := sdk.Override{
		Path:      path,
		Value:     nil,
		Condition: condition,
	}
	if _, err := o.parseCondition(); err != nil {
		return sdk.Override{}, fmt.Errorf("validating override: %w", err)
	}
	return o, nil
}
