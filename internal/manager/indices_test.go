package manager

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIndexCompositionsByDependency(t *testing.T) {
	indexer := indexCompositionsByDependency()

	t.Run("non-composition object returns nil", func(t *testing.T) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
		assert.Nil(t, indexer(pod))
	})

	t.Run("composition with no dependencies", func(t *testing.T) {
		comp := &apiv1.Composition{
			Spec: apiv1.CompositionSpec{},
		}
		assert.Nil(t, indexer(comp))
	})

	t.Run("composition with one dependency", func(t *testing.T) {
		comp := &apiv1.Composition{
			Spec: apiv1.CompositionSpec{
				DependsOn: []apiv1.CompositionDependency{
					{Namespace: "ns1", Name: "dep1"},
				},
			},
		}
		keys := indexer(comp)
		assert.Equal(t, []string{"ns1/dep1"}, keys)
	})

	t.Run("composition with multiple dependencies", func(t *testing.T) {
		comp := &apiv1.Composition{
			Spec: apiv1.CompositionSpec{
				DependsOn: []apiv1.CompositionDependency{
					{Namespace: "ns1", Name: "dep1"},
					{Namespace: "ns2", Name: "dep2"},
				},
			},
		}
		keys := indexer(comp)
		assert.Equal(t, []string{"ns1/dep1", "ns2/dep2"}, keys)
	})
}
