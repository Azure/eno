package reconciliation

import (
	"testing"

	"github.com/Azure/eno/internal/resource"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
)

// TestResourceLabelSelector tests various label selector scenarios
func TestResourceLabelSelector(t *testing.T) {
	tests := []struct {
		name       string
		selector   labels.Selector
		labels     map[string]string
		shouldRun  bool
		customDesc string
	}{
		{
			name:     "matching labels should run",
			selector: labels.SelectorFromSet(labels.Set{"app": "selected"}), 
			labels:   map[string]string{"app": "selected"},
			shouldRun: true,
			customDesc: "Simple exact match",
		},
		{
			name:     "non-matching labels should be filtered out",
			selector: labels.SelectorFromSet(labels.Set{"app": "selected"}),
			labels:   map[string]string{"app": "not-selected"},
			shouldRun: false,
			customDesc: "Simple non-matching value",
		},
		{
			name:     "no labels should be filtered out",
			selector: labels.SelectorFromSet(labels.Set{"app": "selected"}),
			labels:   nil,
			shouldRun: false,
			customDesc: "Resource with no labels",
		},
		{
			name:     "additional labels with matching selector should run",
			selector: labels.SelectorFromSet(labels.Set{"app": "selected"}),
			labels:   map[string]string{"app": "selected", "extra": "value"},
			shouldRun: true,
			customDesc: "Resource with additional unrelated labels",
		},
		{
			name:     "default selector with labels",
			selector: labels.Everything(),
			labels:   map[string]string{"app": "any-value", "env": "prod"},
			shouldRun: true,
			customDesc: "labels.Everything() should match any labeled resource",
		},
		{
			name:     "default selector with no labels",
			selector: labels.Everything(),
			labels:   nil,
			shouldRun: true,
			customDesc: "labels.Everything() should match resources with no labels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a resource with test case labels
			res := &resource.Resource{
				Labels: tt.labels,
				Ref: resource.Ref{
					Name:      "test-resource",
					Namespace: "default",
					Kind:      "ConfigMap",
				},
			}
			
			// Check if selector matches labels
			matches := tt.selector.Matches(labels.Set(res.Labels))
			assert.Equal(t, tt.shouldRun, matches, 
				"Resource with labels %v should%s match selector: %s",
				res.Labels, map[bool]string{true: "", false: " not"}[tt.shouldRun], tt.customDesc)
		})
	}
}

// TestComplexLabelSelectors tests more complex label selector scenarios
func TestComplexLabelSelectors(t *testing.T) {
	tests := []struct {
		name      string
		selector  string
		labels    map[string]string
		shouldRun bool
	}{
		{
			name:      "in operator with multiple values",
			selector:  "environment in (prod, staging)",
			labels:    map[string]string{"environment": "prod"},
			shouldRun: true,
		},
		{
			name:      "in operator with non-matching value",
			selector:  "environment in (prod, staging)",
			labels:    map[string]string{"environment": "dev"},
			shouldRun: false,
		},
		{
			name:      "multiple requirements - all match",
			selector:  "app=web,tier=frontend",
			labels:    map[string]string{"app": "web", "tier": "frontend"},
			shouldRun: true,
		},
		{
			name:      "multiple requirements - partial match",
			selector:  "app=web,tier=frontend",
			labels:    map[string]string{"app": "web", "tier": "backend"},
			shouldRun: false,
		},
		{
			name:      "multiple requirements with extra labels",
			selector:  "app=web,tier=frontend",
			labels:    map[string]string{"app": "web", "tier": "frontend", "extra": "value"},
			shouldRun: true,
		},
		{
			name:      "not equals operator",
			selector:  "environment!=prod",
			labels:    map[string]string{"environment": "staging"},
			shouldRun: true,
		},
		{
			name:      "not equals operator - non-matching",
			selector:  "environment!=prod",
			labels:    map[string]string{"environment": "prod"},
			shouldRun: false,
		},
		{
			name:      "exists operator",
			selector:  "component",
			labels:    map[string]string{"component": "any-value"},
			shouldRun: true,
		},
		{
			name:      "exists operator - missing key",
			selector:  "component",
			labels:    map[string]string{"other": "value"},
			shouldRun: false,
		},
		{
			name:      "selector with comma - OR relationship",
			selector:  "component in (frontend,backend)",
			labels:    map[string]string{"component": "frontend"},
			shouldRun: true,
		},
		{
			name:      "complex expression with both AND and OR",
			selector:  "environment=prod,role in (web,api)",
			labels:    map[string]string{"environment": "prod", "role": "api"},
			shouldRun: true,
		},
		{
			name:      "complex expression - one clause not matching",
			selector:  "environment=prod,role in (web,api)",
			labels:    map[string]string{"environment": "staging", "role": "api"},
			shouldRun: false,
		},
		{
			name:      "selector with does not exist operator",
			selector:  "!environment",
			labels:    map[string]string{"app": "service"},
			shouldRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the selector string
			selector, err := labels.Parse(tt.selector)
			assert.NoError(t, err, "Should parse selector without error")
			
			// Create a resource with the test labels
			res := &resource.Resource{
				Labels: tt.labels,
			}
			
			// Check if the selector matches the labels
			matches := selector.Matches(labels.Set(res.Labels))
			assert.Equal(t, tt.shouldRun, matches, 
				"Resource with labels %v should%s match selector %s", 
				res.Labels, map[bool]string{true: "", false: " not"}[tt.shouldRun], tt.selector)
		})
	}
}