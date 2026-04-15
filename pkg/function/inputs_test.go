package function

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestInputReader(t *testing.T) {
	input := bytes.NewBufferString(`{ "items": [{ "apiVersion": "v1", "kind": "ConfigMap", "metadata": { "name": "test-cm", "annotations": { "eno.azure.io/input-key": "foo" } } }] }`)
	r, err := NewInputReader(input)
	require.NoError(t, err)

	// Found
	cm := &corev1.ConfigMap{}
	err = ReadInput(r, "foo", cm)
	require.NoError(t, err)
	assert.Equal(t, "test-cm", cm.Name)
	assert.Equal(t, "test-cm", r.All()["foo"].GetName())

	// Missing
	err = ReadInput(r, "bar", cm)
	require.EqualError(t, err, "input \"bar\": input not found")
}

func TestNewInputReader(t *testing.T) {
	t.Run("treat empty input (EOF) as empty resource list", func(t *testing.T) {
		input := bytes.NewBufferString("")
		r, err := NewInputReader(input)
		require.NoError(t, err)
		assert.Equal(t, 0, len(r.resources.Items))
	})
}

func TestIsOptional(t *testing.T) {
	t.Run("returns true when input is in FunctionConfig optionalRefs", func(t *testing.T) {
		input := bytes.NewBufferString(`{
			"functionConfig": {
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"optionalRefs": ["missing-optional", "present-optional"]
			},
			"items": [{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "test-cm",
					"annotations": {
						"eno.azure.io/input-key": "required-input"
					}
				}
			}]
		}`)
		r, err := NewInputReader(input)
		require.NoError(t, err)
		assert.True(t, r.IsOptional("missing-optional"))
		assert.True(t, r.IsOptional("present-optional"))
		assert.False(t, r.IsOptional("required-input"))
	})

	t.Run("returns false when input not in optionalRefs", func(t *testing.T) {
		input := bytes.NewBufferString(`{
			"functionConfig": {
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"optionalRefs": ["optional-input"]
			},
			"items": [{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "test-cm",
					"annotations": {
						"eno.azure.io/input-key": "some-input"
					}
				}
			}]
		}`)
		r, err := NewInputReader(input)
		require.NoError(t, err)
		assert.False(t, r.IsOptional("nonexistent"))
		assert.False(t, r.IsOptional("some-input"))
	})

	t.Run("returns false when no FunctionConfig", func(t *testing.T) {
		input := bytes.NewBufferString(`{
			"items": [{
				"apiVersion": "v1",
				"kind": "ConfigMap",
				"metadata": {
					"name": "test-cm",
					"annotations": {
						"eno.azure.io/input-key": "some-input"
					}
				}
			}]
		}`)
		r, err := NewInputReader(input)
		require.NoError(t, err)
		assert.False(t, r.IsOptional("some-input"))
	})
}
