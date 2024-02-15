package io

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOutputs(t *testing.T) {
	resource := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "some-name",
		},
	}}
	expectedOut := "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"some-name\"}}]}\n"

	b := &bytes.Buffer{}
	w := newOutputWriterToWriter(b)

	err := w.Write(resource)
	require.NoError(t, err)
	require.Equal(t, 0, b.Len(), "should not write until contents are committed")

	err = w.Commit()
	require.NoError(t, err)
	require.Greater(t, b.Len(), 0, "should write to writter after committing contents")
	require.Equal(t, expectedOut, b.String())

	err = w.Write(resource)
	require.ErrorIs(t, err, ErrWriterIsCommitted, "should fail writing after committing")
}
