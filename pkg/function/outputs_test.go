package function

import (
	"bytes"
	"testing"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
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
	w.AddResult(&krmv1.Result{Message: "test message", Severity: krmv1.ResultSeverityError})
	assert.Equal(t, 0, out.Len())

	require.NoError(t, w.Write())
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"creationTimestamp\":null,\"name\":\"test-cm\"}}],\"results\":[{\"message\":\"test message\",\"severity\":\"error\"}]}\n", out.String())

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
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"extra-val\":\"value from munge function\"},\"kind\":\"ConfigMap\",\"metadata\":{\"creationTimestamp\":null,\"name\":\"test-cm\"}}]}\n", out.String())
}
