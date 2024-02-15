package io

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestConversion(t *testing.T) {
	resource := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name": "some-name",
		},
		"data": map[string]interface{}{
			"some": "data",
		},
	}}
	expectedOut := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "some-name",
		},
		Data: map[string]string{
			"some": "data",
		},
	}

	res, err := Convert[corev1.ConfigMap](resource)
	require.NoError(t, err)
	require.Equal(t, expectedOut, res)
}
