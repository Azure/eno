package reconciliation

import (
	"testing"

	"github.com/Azure/eno/internal/resource"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/labels"
)

func TestResourceLabelSelectorFilter(t *testing.T) {
	// Create a controller with a resource selector
	controller := &Controller{
		resourceSelector: labels.SelectorFromSet(labels.Set{"app": "selected"}),
	}
	
	// Test cases for different label configurations
	tests := []struct {
		name      string
		labels    map[string]string
		shouldRun bool
	}{
		{
			name:      "matching labels should run",
			labels:    map[string]string{"app": "selected"},
			shouldRun: true,
		},
		{
			name:      "non-matching labels should be filtered out",
			labels:    map[string]string{"app": "not-selected"},
			shouldRun: false,
		},
		{
			name:      "no labels should be filtered out",
			labels:    nil,
			shouldRun: false,
		},
		{
			name:      "additional labels with matching selector should run",
			labels:    map[string]string{"app": "selected", "extra": "value"},
			shouldRun: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a resource with the test case labels
			res := &resource.Resource{
				Labels: tt.labels,
				Ref: resource.Ref{
					Name:      "test",
					Namespace: "default",
					Group:     "",
					Kind:      "ConfigMap",
				},
			}
			
			// Check if the resource is filtered based on labels
			if tt.shouldRun {
				assert.True(t, controller.resourceSelector.Matches(labels.Set(res.Labels)), 
					"Resource with labels %v should match selector", res.Labels)
			} else {
				assert.False(t, controller.resourceSelector.Matches(labels.Set(res.Labels)), 
					"Resource with labels %v should not match selector", res.Labels)
			}
		})
	}
}

func TestDefaultResourceSelector(t *testing.T) {
	// Create a controller with no resource selector (should select everything)
	controller := &Controller{
		resourceSelector: labels.Everything(),
	}
	
	// All resources should be selected when using the default selector
	tests := []struct {
		name   string
		labels map[string]string
	}{
		{
			name:   "with labels",
			labels: map[string]string{"app": "any-value"},
		},
		{
			name:   "with no labels",
			labels: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a resource with the test case labels
			res := &resource.Resource{
				Labels: tt.labels,
				Ref: resource.Ref{
					Name:      "test",
					Namespace: "default",
					Group:     "",
					Kind:      "ConfigMap",
				},
			}
			
			// Default selector should match everything
			assert.True(t, controller.resourceSelector.Matches(labels.Set(res.Labels)), 
				"Default resource selector should match all resources")
		})
	}
}