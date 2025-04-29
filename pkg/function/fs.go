package function

import (
	"fmt"
	"io"
	"os"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ReadManifest reads a YAML file from disk and parses each document into an unstructured object.
func ReadManifest(path string) ([]client.Object, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	var objects []client.Object
	decoder := yaml.NewYAMLOrJSONDecoder(file, 1024)
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				return objects, nil
			}
			return nil, fmt.Errorf("decoding yaml: %w", err)
		}
		objects = append(objects, obj)
	}
}
