package io

import (
	"bytes"
	"testing"

	"github.com/Azure/eno/pkg/inputs"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestInputs(t *testing.T) {
	testIn := `
	{
		"apiVersion": "config.kubernetes.io/v1",
		"kind": "ResourceList",
		"items": [
			{"apiVersion":"v1", "kind": "ConfigMap", "metadata": {"name": "some-name", "annotations": {"eno.azure.io/input-name": "in-name"}}}
		]
	}
	`
	testInName := "in-name"
	expectedOut := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "some-name",
			"annotations": map[string]interface{}{
				inputs.InputNameAnnotationKey: "in-name",
			},
		},
	}}

	r, err := newInputReaderFromReader(bytes.NewBufferString(testIn))
	require.NotNil(t, r)
	require.NoError(t, err)

	res, err := r.GetInput(testInName)
	require.NoError(t, err)
	require.Equal(t, expectedOut, res)

	_, err = r.GetInput("non-existing-input")
	require.ErrorIs(t, err, ErrInputNotFound)
}
