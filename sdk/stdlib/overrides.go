package stdlib

import (
	"fmt"

	"github.com/Azure/eno/sdk"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ReplaceIf creates an override that sets eno.azure.io/replace when the given condition is truthy.
// Useful for conditionally replacing fields that were contributed to the resource by other clients.
func ReplaceIf(condition string) (sdk.Override, error) {
	return sdk.Override{
		Path:      `self.metadata.annotations["eno.azure.io/replace"]`,
		Value:     "true",
		Condition: condition,
	}, nil
}

// AllowVPA lets another client (like VPA) raise resources/requests for a given container.
// Eno will only replace the container's values when they are missing or less than `min`.
func AllowVPA(container string, min corev1.ResourceRequirements) ([]sdk.Override, error) {
	overrides := []sdk.Override{}
	requirementsMap := map[string]corev1.ResourceList{
		"requests": min.Requests,
		"limits":   min.Limits,
	}

	for name, resourceList := range requirementsMap {
		for rtype, value := range resourceList {
			if value.IsZero() {
				continue // skip zero values
			}
			o, err := allowVPA(container, rtype.String(), name, value)
			if err != nil {
				return nil, fmt.Errorf("creating override for %s: %w", name, err)
			}
			overrides = append(overrides, o)
		}
	}

	return overrides, nil
}

func allowVPA(container, resourceType, reqOrLimits string, min resource.Quantity) (sdk.Override, error) {
	const cel = `!pathManagedByEno && self.spec.template.spec.containers.exists(c, c.name == '%s' && has(c.resources.%s) && '%s' in c.resources.%s && compareResourceQuantities(c.resources.%s['%s'], '%s') >= 0)`
	condition := fmt.Sprintf(cel, container, reqOrLimits, resourceType, reqOrLimits, reqOrLimits, resourceType, min.String())
	return sdk.Override{
		Path:      fmt.Sprintf("self.spec.template.spec.containers[name='%s'].resources.%s.%s", container, reqOrLimits, resourceType),
		Value:     nil,
		Condition: condition,
	}, nil
}
