package function

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOutputWriter(t *testing.T) {
	out := bytes.NewBuffer(nil)
	w := NewOutputWriter(out, nil)

	cm := &corev1.ConfigMap{}
	cm.Name = "test-cm"

	require.NoError(t, w.Add(nil))
	require.NoError(t, w.Add(cm))
	assert.Equal(t, 0, out.Len())

	require.NoError(t, w.Write())
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"metadata\":{\"creationTimestamp\":null,\"name\":\"test-cm\"}}]}\n", out.String())

	require.Error(t, w.Add(nil))
}

func TestOutputWriterMunge(t *testing.T) {
	out := bytes.NewBuffer(nil)
	w := NewOutputWriter(out, func(u *unstructured.Unstructured) {
		unstructured.SetNestedField(u.Object, "value from munge function", "data", "extra-val")
	})

	cm := &corev1.ConfigMap{}
	cm.Name = "test-cm"

	require.NoError(t, w.Add(cm))
	require.NoError(t, w.Write())
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"data\":{\"extra-val\":\"value from munge function\"},\"metadata\":{\"creationTimestamp\":null,\"name\":\"test-cm\"}}]}\n", out.String())
}
