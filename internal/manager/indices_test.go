package manager

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIndexCompositionsByDependency(t *testing.T) {
	indexer := indexCompositionsByDependency()

	tests := []struct {
		name     string
		comp     *apiv1.Composition
		expected []string
	}{
		{
			name: "no dependencies returns nil",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-a", Namespace: "ns1"},
			},
			expected: nil,
		},
		{
			name: "one dep same namespace",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-b", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "comp-a", Namespace: "ns1"},
					},
				},
			},
			expected: []string{"ns1/comp-a"},
		},
		{
			name: "one dep cross namespace",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-b", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "comp-a", Namespace: "ns2"},
					},
				},
			},
			expected: []string{"ns2/comp-a"},
		},
		{
			name: "empty namespace defaults to composition namespace",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-b", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "comp-a", Namespace: ""},
					},
				},
			},
			expected: []string{"ns1/comp-a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := indexer(tt.comp)
			assert.Equal(t, tt.expected, result)
		})
	}
}
