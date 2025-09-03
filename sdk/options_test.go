package sdk

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// some overlap with ExampleMain_withMungers
func TestCompositeMunger(t *testing.T) {
	// Create test munge functions
	addLabelMunger := func(obj *unstructured.Unstructured) {
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["test-label"] = "test-value"
		obj.SetLabels(labels)
	}

	addAnnotationMunger := func(obj *unstructured.Unstructured) {
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["test-annotation"] = "test-value"
		obj.SetAnnotations(annotations)
	}

	outBuf := &bytes.Buffer{}
	inBuf := bytes.NewBufferString(`{"items": []}`)

	// Test function that returns a simple pod
	fn := func(inputs struct{}) ([]client.Object, error) {
		pod := &corev1.Pod{}
		pod.Name = "test-pod"
		pod.Namespace = "default"
		return []client.Object{pod}, nil
	}

	// Process options
	opts := &mainConfig{}
	WithMunger(addLabelMunger)(opts)
	WithMunger(addAnnotationMunger)(opts)

	require.NoError(t, main(fn, opts, inBuf, outBuf))

	// Verify that both mungers were applied
	output := outBuf.String()
	assert.Contains(t, output, "test-label")
	assert.Contains(t, output, "test-value")
	assert.Contains(t, output, "test-annotation")
}
