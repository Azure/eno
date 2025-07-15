package loader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestLoadObjects(t *testing.T) {
	// Create a scheme with common types
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, extv1.AddToScheme(scheme))

	testDataDir := filepath.Join("testdata", "mixed")

	t.Run("load mixed objects", func(t *testing.T) {
		objects, err := LoadObjects(testDataDir, scheme)
		require.NoError(t, err)
		assert.NotEmpty(t, objects)

		// Check that we got different types of objects
		var crdCount, configMapCount int
		for _, obj := range objects {
			switch obj.(type) {
			case *extv1.CustomResourceDefinition:
				crdCount++
			case *apiv1.ConfigMap:
				configMapCount++
			}
		}

		assert.Greater(t, crdCount, 0, "should have loaded at least one CRD")
		assert.Greater(t, configMapCount, 0, "should have loaded at least one ConfigMap")
	})

	t.Run("nil scheme", func(t *testing.T) {
		_, err := LoadObjects(testDataDir, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "scheme is required")
	})

	t.Run("nonexistent folder", func(t *testing.T) {
		_, err := LoadObjects("nonexistent", scheme)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "folder does not exist")
	})

	t.Run("empty folder", func(t *testing.T) {
		emptyDir := filepath.Join("testdata", "empty")
		err := os.MkdirAll(emptyDir, 0755)
		require.NoError(t, err)
		defer os.RemoveAll(emptyDir)

		objects, err := LoadObjects(emptyDir, scheme)
		require.NoError(t, err)
		assert.Empty(t, objects)
	})

	t.Run("unregistered type", func(t *testing.T) {
		// Create a minimal scheme without all types
		minimalScheme := runtime.NewScheme()
		require.NoError(t, apiv1.AddToScheme(minimalScheme))
		// Don't add extv1 to test missing type

		_, err := LoadObjects(testDataDir, minimalScheme)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create object for GVK")
	})
}

func TestMarshalBytesToObjects(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	t.Run("empty bytes", func(t *testing.T) {
		objects, err := marshalBytesToObjects([]byte{}, scheme)
		require.NoError(t, err)
		assert.Empty(t, objects)
	})

	t.Run("valid configmap", func(t *testing.T) {
		configMapYAML := `
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
  namespace: default
data:
  key1: value1
`
		objects, err := marshalBytesToObjects([]byte(configMapYAML), scheme)
		require.NoError(t, err)
		require.Len(t, objects, 1)

		cm, ok := objects[0].(*apiv1.ConfigMap)
		require.True(t, ok)
		assert.Equal(t, "test-config", cm.Name)
		assert.Equal(t, "value1", cm.Data["key1"])
	})

	t.Run("invalid YAML", func(t *testing.T) {
		invalidYAML := []byte("invalid: yaml: content: [")
		_, err := marshalBytesToObjects(invalidYAML, scheme)
		assert.Error(t, err)
	})
}

func TestIsYAMLOrJSONFile(t *testing.T) {
	testCases := []struct {
		filename string
		expected bool
	}{
		{"test.yaml", true},
		{"test.yml", true},
		{"test.json", true},
		{"test.txt", false},
		{"test.go", false},
		{"test", false},
		{"/path/to/file.yaml", true},
		{"/path/to/file.xml", false},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			result := isYAMLOrJSONFile(tc.filename)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestLoadObjects_Success(t *testing.T) {
	// Create a scheme with common types
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, extv1.AddToScheme(scheme))

	testDataDir := filepath.Join("testdata", "mixed")

	// Act
	result, err := LoadObjects(testDataDir, scheme)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	assert.Len(t, result, 3, "Should return exactly 3 objects (CRD, ConfigMap, Secret)")

	// Verify expected object types are present
	var crdCount, configMapCount, secretCount int
	for _, obj := range result {
		switch obj.(type) {
		case *extv1.CustomResourceDefinition:
			crdCount++
		case *apiv1.ConfigMap:
			configMapCount++
		case *apiv1.Secret:
			secretCount++
		}
	}

	assert.Equal(t, 1, crdCount, "Should have exactly 1 CRD")
	assert.Equal(t, 1, configMapCount, "Should have exactly 1 ConfigMap")
	assert.Equal(t, 1, secretCount, "Should have exactly 1 Secret")
}

func TestLoadObjects_VerifyExpectedObjects(t *testing.T) {
	// Create a scheme with common types
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, extv1.AddToScheme(scheme))

	testDataDir := filepath.Join("testdata", "mixed")

	// Act
	result, err := LoadObjects(testDataDir, scheme)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Verify expected object names are present
	expectedObjects := map[string]string{
		"compositions.eno.azure.io": "CustomResourceDefinition",
		"app-config":                "ConfigMap",
		"app-secrets":               "Secret",
	}

	foundObjects := make(map[string]string)
	for _, obj := range result {
		switch typedObj := obj.(type) {
		case *extv1.CustomResourceDefinition:
			foundObjects[typedObj.Name] = "CustomResourceDefinition"
		case *apiv1.ConfigMap:
			foundObjects[typedObj.Name] = "ConfigMap"
		case *apiv1.Secret:
			foundObjects[typedObj.Name] = "Secret"
		}
	}

	for expectedName, expectedType := range expectedObjects {
		foundType, exists := foundObjects[expectedName]
		assert.True(t, exists, "Should contain object: %s", expectedName)
		assert.Equal(t, expectedType, foundType, "Object %s should be of type %s", expectedName, expectedType)
	}
}

func TestLoadObjects_VerifyObjectDetails(t *testing.T) {
	// Create a scheme with common types
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, extv1.AddToScheme(scheme))

	testDataDir := filepath.Join("testdata", "mixed")

	// Act
	result, err := LoadObjects(testDataDir, scheme)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Verify specific object details
	for _, obj := range result {
		switch typedObj := obj.(type) {
		case *extv1.CustomResourceDefinition:
			assert.Equal(t, "compositions.eno.azure.io", typedObj.Name)
			assert.Equal(t, "eno.azure.io", typedObj.Spec.Group)
			assert.Equal(t, "Composition", typedObj.Spec.Names.Kind)
		case *apiv1.ConfigMap:
			assert.Equal(t, "app-config", typedObj.Name)
			assert.Equal(t, "default", typedObj.Namespace)
			assert.Contains(t, typedObj.Data, "database_url")
			assert.Equal(t, "postgresql://localhost:5432/myapp", typedObj.Data["database_url"])
		case *apiv1.Secret:
			assert.Equal(t, "app-secrets", typedObj.Name)
			assert.Equal(t, "default", typedObj.Namespace)
			assert.Equal(t, apiv1.SecretTypeOpaque, typedObj.Type)
			assert.Contains(t, typedObj.Data, "username")
		}
	}
}

func TestLoadObjects_EmptyFolder(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	// Arrange
	tempDir := t.TempDir()

	// Act
	result, err := LoadObjects(tempDir, scheme)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestLoadObjects_FolderWithNonYAMLFiles(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, extv1.AddToScheme(scheme))

	// Arrange
	tempDir := t.TempDir()

	// Create non-YAML files
	err := os.WriteFile(filepath.Join(tempDir, "readme.txt"), []byte("This is a readme"), 0644)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(tempDir, "script.sh"), []byte("#!/bin/bash\necho hello"), 0644)
	require.NoError(t, err)

	// Create one valid YAML file
	configMapYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
data:
  key: value`
	err = os.WriteFile(filepath.Join(tempDir, "config.yaml"), []byte(configMapYAML), 0644)
	require.NoError(t, err)

	// Act
	result, err := LoadObjects(tempDir, scheme)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 1, "Should only load the YAML file")

	cm, ok := result[0].(*apiv1.ConfigMap)
	require.True(t, ok)
	assert.Equal(t, "test-config", cm.Name)
}

func TestMarshalBytesToObjects_MultipleSeparatedObjects(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	multipleObjectsYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: config1
data:
  key1: value1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: config2
data:
  key2: value2`

	// Act
	objects, err := marshalBytesToObjects([]byte(multipleObjectsYAML), scheme)

	// Assert
	require.NoError(t, err)
	assert.Len(t, objects, 2)

	cm1, ok := objects[0].(*apiv1.ConfigMap)
	require.True(t, ok)
	assert.Equal(t, "config1", cm1.Name)
	assert.Equal(t, "value1", cm1.Data["key1"])

	cm2, ok := objects[1].(*apiv1.ConfigMap)
	require.True(t, ok)
	assert.Equal(t, "config2", cm2.Name)
	assert.Equal(t, "value2", cm2.Data["key2"])
}

func TestMarshalBytesToObjects_WithComments(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	yamlWithComments := `# This is a comment
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
  # Another comment
data:
  key1: value1  # Inline comment`

	// Act
	objects, err := marshalBytesToObjects([]byte(yamlWithComments), scheme)

	// Assert
	require.NoError(t, err)
	assert.Len(t, objects, 1)

	cm, ok := objects[0].(*apiv1.ConfigMap)
	require.True(t, ok)
	assert.Equal(t, "test-config", cm.Name)
	assert.Equal(t, "value1", cm.Data["key1"])
}

func TestMarshalBytesToObjects_JSONFormat(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	configMapJSON := `{
  "apiVersion": "v1",
  "kind": "ConfigMap",
  "metadata": {
    "name": "test-config-json"
  },
  "data": {
    "key1": "value1"
  }
}`

	// Act
	objects, err := marshalBytesToObjects([]byte(configMapJSON), scheme)

	// Assert
	require.NoError(t, err)
	assert.Len(t, objects, 1)

	cm, ok := objects[0].(*apiv1.ConfigMap)
	require.True(t, ok)
	assert.Equal(t, "test-config-json", cm.Name)
	assert.Equal(t, "value1", cm.Data["key1"])
}

func TestMarshalBytesToObjects_MissingAPIVersion(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	invalidYAML := `kind: ConfigMap
metadata:
  name: test-config
data:
  key1: value1`

	// Act
	objects, err := marshalBytesToObjects([]byte(invalidYAML), scheme)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, objects, "Should skip objects without apiVersion")
}

func TestMarshalBytesToObjects_MissingKind(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	invalidYAML := `apiVersion: v1
metadata:
  name: test-config
data:
  key1: value1`

	// Act
	objects, err := marshalBytesToObjects([]byte(invalidYAML), scheme)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, objects, "Should skip objects without kind")
}

func TestMarshalBytesToObjects_UnknownType(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	// Don't register the CRD types

	crdYAML := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com`

	// Act
	_, err := marshalBytesToObjects([]byte(crdYAML), scheme)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create object for GVK")
}
