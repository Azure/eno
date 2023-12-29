package synthesis

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildResourceSlicesOverflow(t *testing.T) {
	outputs := []*unstructured.Unstructured{}
	for i := 0; i < 16; i++ {
		outputs = append(outputs, &unstructured.Unstructured{})
	}

	slices, err := buildResourceSlices(&apiv1.Composition{}, outputs, 20)
	require.NoError(t, err)
	assert.Len(t, slices, 4)
}

func TestBuildInputsJson(t *testing.T) {
	cm := &corev1.ConfigMap{}
	cm.Name = "test-cm"
	cm.Namespace = "test-namespace"

	client := fake.NewFakeClient(cm)
	e := &execController{client: client}

	comp := &apiv1.Composition{}
	comp.Spec.Inputs = []apiv1.InputRef{
		{}, // no resource reference - should be dropped
		{Resource: &apiv1.ResourceInputRef{
			APIVersion: "v1",
			Kind:       "ConfigMap",
			Name:       cm.Name,
			Namespace:  cm.Namespace,
		}},
	}

	js, err := e.buildInputsJson(testutil.NewContext(t), comp)
	require.NoError(t, err)
	assert.Equal(t, "[{\"apiVersion\":\"v1\",\"kind\":\"ConfigMap\",\"metadata\":{\"creationTimestamp\":null,\"name\":\"test-cm\",\"namespace\":\"test-namespace\",\"resourceVersion\":\"999\"}}]", string(js))
}
