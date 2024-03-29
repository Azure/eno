package resource

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSliceOverflow(t *testing.T) {
	outputs := []*unstructured.Unstructured{}
	for i := 0; i < 16; i++ {
		outputs = append(outputs, &unstructured.Unstructured{})
	}

	slices, err := Slice(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 20)
	require.NoError(t, err)
	assert.Len(t, slices, 4)
}

func TestSliceTombstonesBasics(t *testing.T) {
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

	slices, err := Slice(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.False(t, slices[0].Spec.Resources[0].Deleted)

	// Remove the resource - initial tombstone record is created
	slices, err = Slice(&apiv1.Composition{}, slices, []*unstructured.Unstructured{}, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.True(t, slices[0].Spec.Resources[0].Deleted)

	// The actual resource hasn't been reconciled (deleted) yet, so the tombstone will persist in new states
	slices, err = Slice(&apiv1.Composition{}, slices, []*unstructured.Unstructured{}, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	assert.True(t, slices[0].Spec.Resources[0].Deleted)

	// The tombstone is removed once it has been reconciled
	slices[0].Status.Resources = []apiv1.ResourceState{{Reconciled: true}}
	slices, err = Slice(&apiv1.Composition{}, slices, []*unstructured.Unstructured{}, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 0)
}

func TestSliceReconcileInterval(t *testing.T) {
	outputs := []*unstructured.Unstructured{{
		Object: map[string]interface{}{
			"kind":       "Test",
			"apiVersion": "mygroup/v1",
			"metadata": map[string]interface{}{
				"name":      "test-resource",
				"namespace": "test-ns",
				"annotations": map[string]interface{}{
					"eno.azure.io/reconcile-interval": "10s",
				},
			},
		},
	}}

	// The reconcile interval is passed from the resource itself to its manifest representation
	slices, err := Slice(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 1)
	require.NotNil(t, slices[0].Spec.Resources[0].ReconcileInterval)
	assert.Equal(t, time.Second*10, slices[0].Spec.Resources[0].ReconcileInterval.Duration)                                                                                          // it's in the manifest
	assert.Equal(t, "{\"apiVersion\":\"mygroup/v1\",\"kind\":\"Test\",\"metadata\":{\"name\":\"test-resource\",\"namespace\":\"test-ns\"}}\n", slices[0].Spec.Resources[0].Manifest) // it's not in the resource itself
}

func TestSliceTombstonesVersionSemantics(t *testing.T) {
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
	slices, err := Slice(&apiv1.Composition{}, []*apiv1.ResourceSlice{}, outputs, 100000)
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
	slices, err = Slice(&apiv1.Composition{}, slices, outputs, 100000)
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
	slices, err = Slice(&apiv1.Composition{}, slices, outputs, 100000)
	require.NoError(t, err)
	require.Len(t, slices, 1)
	require.Len(t, slices[0].Spec.Resources, 2)
}
