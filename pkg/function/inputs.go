package function

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InputReader reads and processes input resources from a KRM ResourceList.
//
// Deprecated: This type will be removed in a future version.
type InputReader struct {
	resources *krmv1.ResourceList
}

// NewDefaultInputReader creates an InputReader that reads from os.Stdin.
//
// Deprecated: This function will be removed in a future version.
func NewDefaultInputReader() (*InputReader, error) {
	return NewInputReader(os.Stdin)
}

// NewInputReader creates an InputReader that reads from the specified reader.
// The reader should provide a JSON-encoded KRM ResourceList.
//
// Deprecated: This function will be removed in a future version.
func NewInputReader(r io.Reader) (*InputReader, error) {
	rl := krmv1.ResourceList{}
	err := json.NewDecoder(r).Decode(&rl)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decoding stdin as krm resource list: %w", err)
	}
	return &InputReader{
		resources: &rl,
	}, nil
}

// ReadInput reads and converts an input resource with the specified key into the provided output object.
// The key is matched against the "eno.azure.io/input-key" annotation on the resources.
// Returns an error if the input with the specified key is not found or if there's an error
// converting the resource to the requested type.
//
// Deprecated: This function will be removed in a future version.
func ReadInput[T client.Object](ir *InputReader, key string, out T) error {
	var found bool
	for _, i := range ir.resources.Items {
		i := i
		if getKey(i) == key {
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(i.Object, out)
			if err != nil {
				return fmt.Errorf("converting item to Input: %w", err)
			}
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("input %q was not found", key)
	}
	return nil
}

// All returns a map of all input resources keyed by their input key.
//
// Deprecated: This method will be removed in a future version.
func (i *InputReader) All() map[string]*unstructured.Unstructured {
	m := map[string]*unstructured.Unstructured{}
	for _, o := range i.resources.Items {
		m[getKey(o)] = o
	}
	return m
}

// getKey returns the input key for a Kubernetes object by reading the "eno.azure.io/input-key" annotation.
func getKey(obj client.Object) string {
	if obj.GetAnnotations() == nil {
		return ""
	}
	return obj.GetAnnotations()["eno.azure.io/input-key"]
}
