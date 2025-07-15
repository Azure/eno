package loader

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const whitespaceBufferSize = 4096

// LoadObjects reads Kubernetes YAML files from the specified folder and returns a slice of client.Object.
// The scheme parameter is required and must have all necessary types registered for the objects you want to load.
func LoadObjects(folder string, scheme *runtime.Scheme) ([]client.Object, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is required")
	}

	// Load files from folder
	folderBytes, err := loadFilesFromFolder(folder)
	if err != nil {
		return nil, fmt.Errorf("failed to load files from folder: %w", err)
	}

	var objects []client.Object

	// Parse each file separately
	for _, fileBytes := range folderBytes {
		fileObjects, err := marshalBytesToObjects(fileBytes, scheme)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal bytes to objects: %w", err)
		}

		objects = append(objects, fileObjects...)
	}

	return objects, nil
}

// marshalBytesToObjects decodes the provided byte slice into a list of Kubernetes objects, ignoring commented sections.
func marshalBytesToObjects(b []byte, scheme *runtime.Scheme) ([]client.Object, error) {
	var ret []client.Object

	if len(b) == 0 {
		return ret, nil // Return empty slice instead of error for consistency
	}

	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), whitespaceBufferSize)

	for {
		// Decode directly into runtime.Unknown which preserves the raw bytes
		var obj runtime.Unknown
		err := dec.Decode(&obj)

		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to decode object: %w", err)
		}

		// Check if the object is empty (handles commented sections)
		if len(obj.Raw) == 0 {
			continue
		}

		// Get the GVK from the Unknown object
		gvk := obj.GetObjectKind().GroupVersionKind()
		if gvk.Empty() {
			// Try to decode just the type metadata to get GVK
			var typeMeta struct {
				APIVersion string `json:"apiVersion" yaml:"apiVersion"`
				Kind       string `json:"kind" yaml:"kind"`
			}
			if err := yaml.Unmarshal(obj.Raw, &typeMeta); err != nil {
				continue // Skip objects we can't parse
			}
			if typeMeta.APIVersion == "" || typeMeta.Kind == "" {
				continue // Skip objects without proper GVK
			}

			gv, err := schema.ParseGroupVersion(typeMeta.APIVersion)
			if err != nil {
				return nil, fmt.Errorf("failed to parse apiVersion %s: %w", typeMeta.APIVersion, err)
			}
			gvk = gv.WithKind(typeMeta.Kind)
		}

		// Create the proper object type using the scheme
		typedObj, err := scheme.New(gvk)
		if err != nil {
			return nil, fmt.Errorf("failed to create object for GVK %s: %w", gvk, err)
		}

		// Decode the raw bytes directly into the typed object
		err = yaml.Unmarshal(obj.Raw, typedObj)
		if err != nil {
			return nil, fmt.Errorf("failed to decode typed object: %w", err)
		}

		// Ensure it implements client.Object
		clientObj, ok := typedObj.(client.Object)
		if !ok {
			return nil, fmt.Errorf("object does not implement client.Object interface")
		}

		ret = append(ret, clientObj)
	}

	return ret, nil
}

// loadFilesFromFolder reads all files from the specified folder and returns them as a slice of byte slices.
func loadFilesFromFolder(folderPath string) ([][]byte, error) {
	var filesBytes [][]byte

	// Check if the folder exists
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("folder does not exist: %s", folderPath)
	}

	// Walk through the directory tree
	err := filepath.Walk(folderPath, func(filePath string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("failed to access path %s: %w", filePath, err)
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Only process YAML/JSON files
		if !isYAMLOrJSONFile(filePath) {
			return nil
		}

		// Read the file
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}

		filesBytes = append(filesBytes, fileBytes)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return filesBytes, nil
}

// isYAMLOrJSONFile checks if the file has a YAML or JSON extension
func isYAMLOrJSONFile(filePath string) bool {
	ext := filepath.Ext(filePath)
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
}
