package cel

import (
	"fmt"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/common/types/ref"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestEvalCompositionBasics(t *testing.T) {
	p, err := Parse("composition.metadata.name")
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"

	val, err := Eval(t.Context(), p, comp, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-comp", val.Value())
}

func TestEvalIntTypeCoersion(t *testing.T) {
	p, err := Parse("int(composition.metadata.name) > 100")
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "123"

	val, err := Eval(t.Context(), p, comp, &unstructured.Unstructured{}, nil)
	require.NoError(t, err)
	assert.Equal(t, true, val.Value())
}

func TestEvalFloatTypeCoersion(t *testing.T) {
	p, err := Parse("double(composition.metadata.name) < 101.9")
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "101.8"

	val, err := Eval(t.Context(), p, comp, &unstructured.Unstructured{}, nil)
	require.NoError(t, err)
	assert.Equal(t, true, val.Value())
}

func TestEvalExtensions(t *testing.T) {
	p, err := Parse("composition.metadata.name.split('-').distinct()")
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "test-test-comp"

	val, err := Eval(t.Context(), p, comp, nil, nil)
	require.NoError(t, err)

	list := val.Value().([]ref.Val)
	assert.Len(t, list, 2)
	assert.Equal(t, "test", list[0].Value())
	assert.Equal(t, "comp", list[1].Value())
}

func TestCompareResourceQuantities(t *testing.T) {
	tests := []struct {
		name     string
		left     string
		right    string
		expected int64
		wantErr  bool
	}{
		// Equal quantities
		{
			name:     "equal decimal values",
			left:     "100m",
			right:    "100m",
			expected: 0,
		},
		{
			name:     "equal values different formats",
			left:     "1",
			right:    "1000m",
			expected: 0,
		},
		{
			name:     "equal memory values",
			left:     "1Gi",
			right:    "1073741824",
			expected: 0,
		},
		{
			name:     "equal zero values",
			left:     "0",
			right:    "0m",
			expected: 0,
		},

		// Less than comparisons
		{
			name:     "left less than right - decimal",
			left:     "100m",
			right:    "200m",
			expected: -1,
		},
		{
			name:     "left less than right - memory",
			left:     "1Mi",
			right:    "1Gi",
			expected: -1,
		},
		{
			name:     "left less than right - mixed formats",
			left:     "0.5",
			right:    "1000m",
			expected: -1,
		},
		{
			name:     "zero less than positive",
			left:     "0",
			right:    "1m",
			expected: -1,
		},

		// Greater than comparisons
		{
			name:     "left greater than right - decimal",
			left:     "200m",
			right:    "100m",
			expected: 1,
		},
		{
			name:     "left greater than right - memory",
			left:     "1Gi",
			right:    "1Mi",
			expected: 1,
		},
		{
			name:     "left greater than right - mixed formats",
			left:     "2",
			right:    "1000m",
			expected: 1,
		},
		{
			name:     "positive greater than zero",
			left:     "1m",
			right:    "0",
			expected: 1,
		},

		// Different units - CPU
		{
			name:     "different cpu units - millis vs whole",
			left:     "500m",
			right:    "1",
			expected: -1,
		},
		{
			name:     "different cpu units - micro vs milli",
			left:     "1000u",
			right:    "1m",
			expected: 0,
		},

		// Different units - Memory
		{
			name:     "Ki vs bytes",
			left:     "1Ki",
			right:    "1024",
			expected: 0,
		},
		{
			name:     "Ti vs Gi",
			left:     "1Ti",
			right:    "1024Gi",
			expected: 0,
		},

		// Scientific notation (using valid K8s format)
		{
			name:     "scientific notation equal",
			left:     "1000m",
			right:    "1",
			expected: 0,
		},

		// Large values
		{
			name:     "large values equal",
			left:     "999999999999999999",
			right:    "999999999999999999",
			expected: 0,
		},
		{
			name:     "large values different",
			left:     "999999999999999998",
			right:    "999999999999999999",
			expected: -1,
		},

		// Fractional values
		{
			name:     "fractional cpu equal",
			left:     "0.5",
			right:    "500m",
			expected: 0,
		},
		{
			name:     "fractional comparison",
			left:     "0.25",
			right:    "0.5",
			expected: -1,
		},

		// Error cases
		{
			name:    "invalid left quantity",
			left:    "invalid",
			right:   "100m",
			wantErr: true,
		},
		{
			name:    "invalid right quantity",
			left:    "100m",
			right:   "invalid",
			wantErr: true,
		},
		{
			name:    "both invalid quantities",
			left:    "invalid1",
			right:   "invalid2",
			wantErr: true,
		},
		{
			name:    "empty left quantity",
			left:    "",
			right:   "100m",
			wantErr: true,
		},
		{
			name:    "empty right quantity",
			left:    "100m",
			right:   "",
			wantErr: true,
		},
		{
			name:    "invalid unit",
			left:    "100xyz",
			right:   "100m",
			wantErr: true,
		},
		{
			name:     "negative quantities",
			left:     "-100m",
			right:    "100m",
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse("compareResourceQuantities(self.left, self.right)")
			require.NoError(t, err)

			obj := &unstructured.Unstructured{
				Object: map[string]any{
					"left":  tt.left,
					"right": tt.right,
				},
			}
			val, err := Eval(t.Context(), p, &apiv1.Composition{}, obj, nil)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			result, ok := val.Value().(int64)
			require.True(t, ok, "expected int64 result, got %T", val.Value())
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestValidHubbleMetrics(t *testing.T) {
	tests := []struct {
		name     string
		metrics  string
		expected bool
	}{
		{
			name:     "valid simple metrics",
			metrics:  "flow tcp dns",
			expected: true,
		},
		{
			name:     "valid metrics with options",
			metrics:  "flow:sourceContext=pod;destinationContext=pod tcp drop",
			expected: true,
		},
		{
			name:     "invalid metric type",
			metrics:  "invalid-metric",
			expected: false,
		},
		{
			name:     "empty string",
			metrics:  "",
			expected: false,
		},
		{
			name:     "valid complex options",
			metrics:  "flow:sourceContext=pod|namespace;destinationContext=workload httpV2:exemplars=true",
			expected: true,
		},
		{
			name:     "valid legacy http metric",
			metrics:  "http:destinationContext=workload-name dns:query",
			expected: true,
		},
		{
			name:     "invalid context value",
			metrics:  "flow:sourceContext=invalid-value",
			expected: false,
		},
		{
			name:     "real world config - basic metrics",
			metrics:  "flow:sourceEgressContext=pod;destinationIngressContext=pod tcp:sourceEgressContext=pod;destinationIngressContext=pod drop:sourceEgressContext=pod;destinationIngressContext=pod dns:sourceEgressContext=pod;destinationIngressContext=pod",
			expected: true,
		},
		{
			name:     "real world config - enhanced observability",
			metrics:  "flow:sourceEgressContext=pod;destinationIngressContext=pod tcp:sourceEgressContext=pod;destinationIngressContext=pod drop:sourceEgressContext=pod;destinationIngressContext=pod dns:query;sourceEgressContext=pod;destinationIngressContext=pod flows-to-world:syn-only;sourceEgressContext=pod;destinationContext=ip",
			expected: true,
		},
		{
			name:     "real world config - minimal setup",
			metrics:  "flow:sourceEgressContext=pod;destinationIngressContext=pod tcp drop:sourceEgressContext=pod;destinationIngressContext=pod dns:sourceEgressContext=pod;destinationIngressContext=pod",
			expected: true,
		},
		{
			name:     "real world config - L7 enabled",
			metrics:  "flow:sourceEgressContext=pod;destinationIngressContext=pod tcp:sourceEgressContext=pod;destinationIngressContext=pod drop:sourceEgressContext=pod;destinationIngressContext=pod dns:sourceEgressContext=pod;destinationIngressContext=pod httpV2:sourceIngressContext=pod;destinationEgressContext=pod kafka:sourceEgressContext=pod;destinationIngressContext=pod",
			expected: true,
		},
		{
			name:     "real world config - L7 with enhanced observability",
			metrics:  "flow:sourceEgressContext=pod;destinationIngressContext=pod tcp:sourceEgressContext=pod;destinationIngressContext=pod drop:sourceEgressContext=pod;destinationIngressContext=pod dns:query;sourceEgressContext=pod;destinationIngressContext=pod flows-to-world:syn-only;sourceEgressContext=pod;destinationContext=ip httpV2:sourceIngressContext=pod;destinationEgressContext=pod kafka:sourceEgressContext=pod;destinationIngressContext=pod",
			expected: true,
		},

		// Edge cases and error conditions
		{
			name:     "whitespace only string",
			metrics:  "   \t   ",
			expected: false,
		},
		{
			name:     "single space",
			metrics:  " ",
			expected: false,
		},
		{
			name:     "metric with empty option",
			metrics:  "flow:",
			expected: false,
		},
		{
			name:     "metric with semicolon but no options",
			metrics:  "flow:;",
			expected: false,
		},
		{
			name:     "metric with empty key-value pair",
			metrics:  "flow:=value",
			expected: false,
		},
		{
			name:     "metric with key but no value",
			metrics:  "flow:sourceContext=",
			expected: false,
		},
		{
			name:     "metric with invalid key",
			metrics:  "flow:invalidKey=pod",
			expected: false,
		},
		{
			name:     "boolean flag with equals sign",
			metrics:  "dns:query=",
			expected: false,
		},
		{
			name:     "valid boolean flag without value",
			metrics:  "dns:query",
			expected: true,
		},
		{
			name:     "invalid boolean flag",
			metrics:  "dns:invalidFlag",
			expected: false,
		},
		{
			name:     "labelsContext with invalid label",
			metrics:  "flow:labelsContext=source_ip,invalid_label",
			expected: false,
		},
		{
			name:     "labelsContext with empty label",
			metrics:  "flow:labelsContext=source_ip,,destination_ip",
			expected: false,
		},
		{
			name:     "context with pipe separator",
			metrics:  "flow:sourceContext=pod|namespace|workload",
			expected: true,
		},
		{
			name:     "context with invalid pipe value",
			metrics:  "flow:sourceContext=pod|invalid-context",
			expected: false,
		},
		{
			name:     "context with empty pipe value",
			metrics:  "flow:sourceContext=pod||namespace",
			expected: false,
		},
		{
			name:     "multiple metrics with one invalid",
			metrics:  "flow dns invalid-metric tcp",
			expected: false,
		},
		{
			name:     "mixed valid and invalid options",
			metrics:  "flow:sourceContext=pod;invalidOption=value",
			expected: false,
		},
		{
			name:     "dns with both valid options",
			metrics:  "dns:query;ignoreAAAA",
			expected: true,
		},
		{
			name:     "flows-to-world with all options",
			metrics:  "flows-to-world:any-drop;port;syn-only",
			expected: true,
		},
		{
			name:     "httpV2 with exemplars boolean",
			metrics:  "httpV2:exemplars=true",
			expected: true,
		},
		{
			name:     "httpV2 with exemplars false",
			metrics:  "httpV2:exemplars=false",
			expected: true,
		},
		{
			name:     "httpV2 with invalid exemplars value",
			metrics:  "httpV2:exemplars=maybe",
			expected: false,
		},
		{
			name:     "labelsContext with all valid labels",
			metrics:  "flow:labelsContext=source_ip,source_namespace,destination_ip,traffic_direction",
			expected: true,
		},
		{
			name:     "case sensitivity test - uppercase metric",
			metrics:  "FLOW",
			expected: false,
		},
		{
			name:     "case sensitivity test - uppercase option",
			metrics:  "flow:SOURCECONTEXT=pod",
			expected: false,
		},
		{
			name:     "special characters in metric name",
			metrics:  "flow-test",
			expected: false,
		},
		{
			name:     "metric with colon but no options after",
			metrics:  "flow: tcp",
			expected: false,
		},
		{
			name:     "option with multiple equals signs",
			metrics:  "flow:sourceContext=pod=extra",
			expected: false,
		},
		{
			name:     "extra whitespace handling",
			metrics:  "  flow : sourceContext = pod ; destinationContext = namespace   tcp  ",
			expected: false,
		},
		{
			name:     "metric name with trailing colon in multi-metric",
			metrics:  "flow: tcp dns",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr := fmt.Sprintf("validHubbleMetrics('%s')", tt.metrics)
			p, err := Parse(expr)
			if err != nil {
				t.Fatalf("failed to parse expression: %v", err)
			}

			result, _, err := p.Eval(map[string]interface{}{})
			if err != nil {
				t.Fatalf("failed to evaluate expression: %v", err)
			}

			if result.Value() != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result.Value())
			}
		})
	}
}
