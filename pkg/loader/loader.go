// Package loader provides utilities for loading and parsing Kubernetes objects from YAML/JSON files.
//
// The loader package supports loading any Kubernetes object type from manifest files,
// provided that the required types are registered in the runtime scheme. The package
// recursively walks through directories to find and process all supported files.
//
// # Basic Usage
//
//	import (
//	    "github.com/Azure/eno/pkg/loader"
//	    apiv1 "k8s.io/api/core/v1"
//	    extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
//	    "k8s.io/apimachinery/pkg/runtime"
//	)
//
//	// Create a scheme with the types you want to load
//	scheme := runtime.NewScheme()
//	apiv1.AddToScheme(scheme)
//	extv1.AddToScheme(scheme)
//
//	// Load objects from a directory (recursively processes subdirectories)
//	objects, err := loader.LoadObjects("/path/to/manifests", scheme)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// # Controller-Runtime Integration
//
//	func loadManifests(mgr manager.Manager) error {
//	    // Use the manager's scheme which already has your types registered
//	    objects, err := loader.LoadObjects("/path/to/manifests", mgr.GetScheme())
//	    if err != nil {
//	        return err
//	    }
//	    // Process objects...
//	    return nil
//	}
//
// # Custom API Types
//
//	import (
//	    "github.com/Azure/eno/pkg/loader"
//	    "k8s.io/apimachinery/pkg/runtime"
//
//	    // Your custom API types
//	    myapiv1 "github.com/example/myoperator/api/v1"
//	)
//
//	// Create a scheme with your custom types
//	scheme := runtime.NewScheme()
//
//	// Register your custom types
//	myapiv1.AddToScheme(scheme)
//
//	// Load manifests that include your custom resources
//	objects, err := loader.LoadObjects("/path/to/custom/manifests", scheme)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// # File Format Support
//
// The package processes files with the following extensions:
//   - .yaml
//   - .yml
//   - .json
//
// # Error Handling
//
// The package provides detailed error messages for common issues:
//   - Missing scheme: "scheme is required"
//   - Folder not found: "folder does not exist: /path"
//   - Unregistered types: "failed to create object for GVK"
//   - Invalid YAML/JSON: "failed to decode object"
//   - Type safety: "object does not implement client.Object interface"
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

// LoadObjects reads Kubernetes YAML/JSON files (.yaml, .yml, .json) from the specified folder and returns a slice of client.Object.
// The scheme parameter is required and must have all necessary types registered for the objects you want to load.
func LoadObjects(folder string, scheme *runtime.Scheme) ([]client.Object, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is required")
	}

	// Check if the folder exists
	if _, err := os.Stat(folder); os.IsNotExist(err) {
		return nil, fmt.Errorf("folder does not exist: %s", folder)
	}

	var objects []client.Object

	// Walk through the directory tree and parse files as we encounter them
	err := filepath.Walk(folder, func(filePath string, info os.FileInfo, err error) error {
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

		// Read and parse the file immediately
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", filePath, err)
		}

		// Parse the file contents immediately
		fileObjects, err := marshalBytesToObjects(fileBytes, scheme)
		if err != nil {
			return fmt.Errorf("failed to marshal bytes to objects from file %s: %w", filePath, err)
		}

		// Append to our results
		objects = append(objects, fileObjects...)

		// fileBytes can now be garbage collected
		return nil
	})

	if err != nil {
		return nil, err
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

// isYAMLOrJSONFile checks if the file has a YAML or JSON extension
func isYAMLOrJSONFile(filePath string) bool {
	ext := filepath.Ext(filePath)
	return ext == ".yaml" || ext == ".yml" || ext == ".json"
}
