package crds

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	apiv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const whitespaceBufferSize = 4096

// Load reads CRD yamls from the specified folder, decodes them, and returns a slice of client.Object.
// If scheme is nil, a default scheme with apiextensions/v1 will be created.
func Load(crdsFolder string, scheme *runtime.Scheme) ([]client.Object, error) {
	// Load files from folder
	folderCrdsBytes, err := loadFilesFromFolder(crdsFolder)
	if err != nil {
		return nil, fmt.Errorf("failed to load CRDs from folder: %w", err)
	}

	var objects []client.Object

	// Parse each file separately to avoid copying
	for _, fileBytes := range folderCrdsBytes {
		crdObjects, err := marshalBytesToCRDs(fileBytes, scheme)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal bytes to CRDs: %w", err)
		}

		for _, crd := range crdObjects {
			objects = append(objects, crd)
		}
	}

	return objects, nil
}

// marshalBytesToObjects decodes the provided byte slice into a list of Kubernetes objects, ignoring commented sections.
func marshalBytesToObjects(b []byte, scheme *runtime.Scheme) ([]client.Object, error) {
	var ret []client.Object

	if len(b) == 0 {
		return ret, nil // Return empty slice instead of error for consistency
	}

	// Create default scheme if none provided
	if scheme == nil {
		scheme = runtime.NewScheme()
		if err := apiv1.AddToScheme(scheme); err != nil {
			return nil, fmt.Errorf("failed to register default scheme: %w", err)
		}
	}

	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), whitespaceBufferSize)

	for {
		// Use a generic map to decode first
		var rawObj map[string]interface{}
		err := dec.Decode(&rawObj)

		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to decode object: %w", err)
		}

		// Check if the object is nil or empty (handles commented sections)
		if rawObj == nil || len(rawObj) == 0 {
			continue
		}

		// Extract GVK from the raw object
		apiVersion, ok := rawObj["apiVersion"].(string)
		if !ok || apiVersion == "" {
			continue // Skip objects without apiVersion
		}

		kind, ok := rawObj["kind"].(string)
		if !ok || kind == "" {
			continue // Skip objects without kind
		}

		// Parse the GVK
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return nil, fmt.Errorf("failed to parse apiVersion %s: %w", apiVersion, err)
		}

		gvk := gv.WithKind(kind)

		// Create the proper object type using the scheme
		typedObj, err := scheme.New(gvk)
		if err != nil {
			return nil, fmt.Errorf("failed to create object for GVK %s: %w", gvk, err)
		}

		// Convert the raw object back to bytes for proper decoding
		rawBytes, err := yaml.Marshal(rawObj)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal raw object: %w", err)
		}

		// Decode the bytes into the typed object
		typedDec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(rawBytes), whitespaceBufferSize)
		err = typedDec.Decode(typedObj)
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

// marshalBytesToCRDs decodes the provided byte slice into a list of CRD objects, ignoring commented sections.
// This is a convenience wrapper around marshalBytesToObjects for CRDs with validation.
func marshalBytesToCRDs(b []byte, scheme *runtime.Scheme) ([]*apiv1.CustomResourceDefinition, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty bytes passed for extracting CRD objects")
	}

	objects, err := marshalBytesToObjects(b, scheme)
	if err != nil {
		return nil, err
	}

	var crds []*apiv1.CustomResourceDefinition
	for _, obj := range objects {
		crd, ok := obj.(*apiv1.CustomResourceDefinition)
		if !ok {
			return nil, fmt.Errorf("expected CustomResourceDefinition, got %T", obj)
		}

		// Check for nil CRD or missing TypeMeta
		if crd == nil {
			continue
		}

		// Validate Kind and APIVersion directly from TypeMeta
		if crd.TypeMeta.Kind != "CustomResourceDefinition" || crd.TypeMeta.APIVersion != "apiextensions.k8s.io/v1" {
			return nil, fmt.Errorf("expected kind CustomResourceDefinition from group apiextensions.k8s.io/v1, got Kind: %s, APIVersion: %s", crd.TypeMeta.Kind, crd.TypeMeta.APIVersion)
		}

		crds = append(crds, crd)
	}

	return crds, nil
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
