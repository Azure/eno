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

	slices, err := buildResourceSlices(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 20)
	require.NoError(t, err)
	assert.Len(t, slices, 4)
}

func TestBuildResourceSlicesTombstonesBasics(t *testing.T) {
	outputs := []*unstructured.Unstructured{{
		Object: map[string]interface{}{
			"kind":       "Test",
			"apiVersion": "mygroup/v1",
			"metadata": map[string]interface{}{
				"name":      "test-resource",
				"namespace": "test-ns",
			},
		},
	}}

	slices, err := buildResourceSlices(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.False(t, slices[0].Spec.Resources[0].Deleted)

	// Remove the resource - initial tombstone record is created
	slices, err = buildResourceSlices(&apiv1.Composition{}, slices, []*unstructured.Unstructured{}, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.True(t, slices[0].Spec.Resources[0].Deleted)

	// The actual resource hasn't been reconciled (deleted) yet, so the tombstone will persist in new states
	slices, err = buildResourceSlices(&apiv1.Composition{}, slices, []*unstructured.Unstructured{}, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.True(t, slices[0].Spec.Resources[0].Deleted)

	// The tombstone is removed once it has been reconciled
	slices[0].Status.Resources = []apiv1.ResourceState{{Reconciled: true}}
	slices, err = buildResourceSlices(&apiv1.Composition{}, slices, []*unstructured.Unstructured{}, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 0)
}

func TestBuildResourceSlicesTombstonesVersionSemantics(t *testing.T) {
	outputs := []*unstructured.Unstructured{{
		Object: map[string]interface{}{
			"kind":       "Test",
			"apiVersion": "mygroup/v1",
			"metadata": map[string]interface{}{
				"name":      "test-resource",
				"namespace": "test-ns",
			},
		},
	}}
	slices, err := buildResourceSlices(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.False(t, slices[0].Spec.Resources[0].Deleted)

	// Upgrade to v2 - tombstone should not be created
	outputs = []*unstructured.Unstructured{{
		Object: map[string]interface{}{
			"kind":       "Test",
			"apiVersion": "mygroup/v2",
			"metadata": map[string]interface{}{
				"name":      "test-resource",
				"namespace": "test-ns",
			},
		},
	}}
	slices, err = buildResourceSlices(&apiv1.Composition{}, slices, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.False(t, slices[0].Spec.Resources[0].Deleted)

	// Change group name - tombstone should be created
	outputs = []*unstructured.Unstructured{{
		Object: map[string]interface{}{
			"kind":       "Test",
			"apiVersion": "mygroup2/v2",
			"metadata": map[string]interface{}{
				"name":      "test-resource",
				"namespace": "test-ns",
			},
		},
	}}
	slices, err = buildResourceSlices(&apiv1.Composition{}, slices, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 2)
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
