package function

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestOutputWriter(t *testing.T) {
	out := bytes.NewBuffer(nil)
	w := NewOutputWriter(out)

	cm := &corev1.ConfigMap{}
	cm.Name = "test-cm"

	require.NoError(t, w.Add(nil))
	require.NoError(t, w.Add(cm))
	assert.Equal(t, 0, out.Len())

	require.NoError(t, w.Write())
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"metadata\":{\"creationTimestamp\":null,\"name\":\"test-cm\"}}]}\n", out.String())

	require.Error(t, w.Add(nil))
}
