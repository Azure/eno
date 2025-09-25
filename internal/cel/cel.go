package cel

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var Env *cel.Env

type FieldMetadata interface {
	ManagedByEno(context.Context, *unstructured.Unstructured) bool
}

func init() {
	initDefaultEnv()
}

func initDefaultEnv() {
	var err error
	Env, err = cel.NewEnv(
		ext.Encoders(),
		ext.Lists(),
		ext.Strings(),
		cel.Variable("self", cel.DynType),
		cel.Variable("composition", cel.DynType),
		cel.Variable("pathManagedByEno", cel.BoolType),
		cel.Function("compareResourceQuantities",
			cel.Overload("compare_resource_quantities_equal_string_string",
				[]*cel.Type{cel.StringType, cel.StringType}, cel.IntType,
				cel.BinaryBinding(compareResources))),
		cel.Function("validHubbleMetrics",
			cel.Overload("valid_hubble_metrics_string",
				[]*cel.Type{cel.StringType}, cel.BoolType,
				cel.UnaryBinding(validateHubbleMetrics))),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to create default CEL environment: %v", err))
	}
}

func Parse(expr string) (cel.Program, error) {
	ast, iss := Env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	return Env.Program(ast, cel.InterruptCheckFrequency(10))
}

func Eval(ctx context.Context, prgm cel.Program, comp *apiv1.Composition, self *unstructured.Unstructured, fm FieldMetadata) (ref.Val, error) {
	args := map[string]any{
		"composition": func() any { return newCompositionMap(comp) }, // cel will only execute this if the composition is referenced in the expression
	}
	if self != nil {
		args["self"] = self.Object
	}
	if fm != nil {
		args["pathManagedByEno"] = func() any { return fm.ManagedByEno(ctx, self) }
	}
	val, _, err := prgm.Eval(args)
	return val, err
}

func newCompositionMap(comp *apiv1.Composition) map[string]any {
	m := map[string]any{
		"name":        comp.Name,
		"namespace":   comp.Namespace,
		"labels":      comp.Labels,
		"annotations": comp.Annotations,
	}

	if comp.ObjectMeta.DeletionTimestamp == nil {
		m["deletionTimestamp"] = nil
	} else {
		m["deletionTimestamp"] = comp.ObjectMeta.DeletionTimestamp.Time.Format(time.RFC3339)
	}

	return map[string]any{"metadata": m}
}

func compareResources(lhs ref.Val, rhs ref.Val) ref.Val {
	lStr, _ := lhs.Value().(string)
	rStr, _ := rhs.Value().(string)

	l, err := resource.ParseQuantity(lStr)
	if err != nil {
		return types.WrapErr(fmt.Errorf("parsing left quantity: %w", err))
	}

	r, err := resource.ParseQuantity(rStr)
	if err != nil {
		return types.WrapErr(fmt.Errorf("parsing right quantity: %w", err))
	}

	return types.Int(l.Cmp(r))
}

// validateHubbleMetrics validates Hubble metrics string according to Cilium documentation.
// See: https://docs.cilium.io/en/stable/observability/metrics/#hubble
// Used in CEL expressions to validate user input for Hubble metrics override annotations
// in the Cilium configmap.
func validateHubbleMetrics(val ref.Val) ref.Val {
	str, ok := val.Value().(string)
	if !ok {
		return types.Bool(false)
	}

	if strings.TrimSpace(str) == "" {
		return types.Bool(false)
	}

	// Valid metric types from Cilium documentation
	validMetrics := map[string]bool{
		"dns":               true,
		"drop":              true,
		"tcp":               true,
		"flow":              true,
		"port-distribution": true,
		"icmp":              true,
		"http":              true, // Legacy metric
		"httpV2":            true, // New metric (cannot be used with http)
		"kafka":             true,
		"flows-to-world":    true,
	}

	// Valid context option keys from official Cilium documentation
	validContextOptions := map[string]bool{
		// Global context options (supported by all metrics)
		"sourceContext":             true,
		"sourceEgressContext":       true,
		"sourceIngressContext":      true,
		"destinationContext":        true,
		"destinationEgressContext":  true,
		"destinationIngressContext": true,
		"labelsContext":             true,

		// Metric-specific options
		"exemplars":  true, // httpV2 only - Include extracted trace IDs in HTTP metrics
		"query":      true, // dns only - Include the query as label "query"
		"ignoreAAAA": true, // dns only - Ignore any AAAA requests/responses
		"any-drop":   true, // flows-to-world only - Count any dropped flows regardless of drop reason
		"port":       true, // flows-to-world only - Include the destination port as label port
		"syn-only":   true, // flows-to-world only - Only count non-reply SYNs for TCP flows
	}

	// Valid context values from official Cilium documentation
	validContextValues := map[string]bool{
		// sourceContext/destinationContext values
		"identity":          true, // All Cilium security identity labels
		"namespace":         true, // Kubernetes namespace name
		"pod":               true, // Kubernetes pod name and namespace name in the form of namespace/pod
		"pod-name":          true, // Kubernetes pod name
		"dns":               true, // All known DNS names of the source or destination (comma-separated)
		"ip":                true, // The IPv4 or IPv6 address
		"reserved-identity": true, // Reserved identity label
		"workload":          true, // Kubernetes pod's workload name and namespace in the form of namespace/workload-name
		"workload-name":     true, // Kubernetes pod's workload name (Deployment, StatefulSet, DaemonSet, etc.)
		"app":               true, // Kubernetes pod's app name, derived from pod labels (app.kubernetes.io/name, k8s-app, or app)

		// Boolean values for options like exemplars
		"true":  true, // for boolean options like exemplars
		"false": true, // for boolean options
	}

	// Valid labelsContext values
	validLabelsContextValues := map[string]bool{
		"source_ip":                 true,
		"source_namespace":          true,
		"source_pod":                true,
		"source_workload":           true,
		"source_workload_kind":      true,
		"source_app":                true,
		"destination_ip":            true,
		"destination_namespace":     true,
		"destination_pod":           true,
		"destination_workload":      true,
		"destination_workload_kind": true,
		"destination_app":           true,
		"traffic_direction":         true,
	}

	// Split metrics by space to handle multiple metrics
	metricEntries := strings.Fields(str)

	for _, entry := range metricEntries {
		// Each entry can be "metric" or "metric:option1;option2;..."
		parts := strings.SplitN(entry, ":", 2)
		metricName := parts[0]

		// Validate metric name
		if !validMetrics[metricName] {
			return types.Bool(false)
		}

		// If there are options, validate them
		if len(parts) > 1 {
			options := strings.Split(parts[1], ";")
			for _, option := range options {
				if !validateHubbleMetricsOption(option, validContextOptions, validContextValues, validLabelsContextValues) {
					return types.Bool(false)
				}
			}
		}
	}

	return types.Bool(true)
}

func validateHubbleMetricsOption(option string, validContextOptions, validContextValues, validLabelsContextValues map[string]bool) bool {
	option = strings.TrimSpace(option)
	if option == "" {
		return false
	}

	// Handle boolean flags (no = sign)
	if validContextOptions[option] {
		return true
	}

	// Handle key=value options
	if strings.Contains(option, "=") {
		parts := strings.SplitN(option, "=", 2)
		if len(parts) != 2 {
			return false
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if !validContextOptions[key] {
			return false
		}

		// Special handling for labelsContext which can have comma-separated values
		if key == "labelsContext" {
			labelValues := strings.Split(value, ",")
			for _, labelValue := range labelValues {
				labelValue = strings.TrimSpace(labelValue)
				if !validLabelsContextValues[labelValue] {
					return false
				}
			}
			return true
		}

		// For context options that can have multiple values separated by |
		if strings.HasSuffix(key, "Context") && key != "labelsContext" {
			contextValues := strings.Split(value, "|")
			for _, contextValue := range contextValues {
				contextValue = strings.TrimSpace(contextValue)
				if !validContextValues[contextValue] {
					return false
				}
			}
			return true
		}

		// For other options, just check if the value is valid
		if !validContextValues[value] {
			return false
		}

		return true
	}

	return false
}
