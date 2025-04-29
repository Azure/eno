package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReadManifestHappyPath(t *testing.T) {
	objects, err := ReadManifest("fixtures/valid.yaml")
	require.NoError(t, err)
	assert.Equal(t, []client.Object{
		&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "myapi.myapp.io/v1",
				"kind":       "Example",
				"metadata": map[string]interface{}{
					"name":      "example",
					"namespace": "default",
				},
			},
		},
		&unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"name":      "example",
					"namespace": "default",
				},
			},
		},
	}, objects)
}

func TestReadManifestInvalidYAML(t *testing.T) {
	objects, err := ReadManifest("fixtures/invalid.yaml")
	require.Error(t, err)
	assert.Empty(t, objects)
}
