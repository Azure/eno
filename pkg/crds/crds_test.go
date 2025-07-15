package crds

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apiv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestCrds_Success(t *testing.T) {
	// Arrange
	crdsFolder := "./fixtures"

	// Act
	result, err := Load(crdsFolder, nil)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	assert.Equal(t, 2, len(result), "Should return exactly 2 CRDs")

	// Verify all returned objects are CRDs
	for i, obj := range result {
		crd, ok := obj.(*apiv1.CustomResourceDefinition)
		require.True(t, ok, "Object %d should be a CustomResourceDefinition", i)
		assert.NotEmpty(t, crd.Name, "CRD %d should have a name", i)
		assert.NotEmpty(t, crd.Spec.Group, "CRD %d should have a group", i)
		assert.Equal(t, "example.com", crd.Spec.Group, "CRD %d should have correct group", i)
	}
}

func TestCrds_WithCustomScheme(t *testing.T) {
	// Arrange
	crdsFolder := "./fixtures"

	// Create a custom scheme and register apiextensions/v1
	customScheme := runtime.NewScheme()
	err := apiv1.AddToScheme(customScheme)
	require.NoError(t, err)

	// Act
	result, err := Load(crdsFolder, customScheme)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)
	assert.Equal(t, 2, len(result), "Should return exactly 2 CRDs")

	// Verify all returned objects are CRDs
	for i, obj := range result {
		crd, ok := obj.(*apiv1.CustomResourceDefinition)
		require.True(t, ok, "Object %d should be a CustomResourceDefinition", i)
		assert.NotEmpty(t, crd.Name, "CRD %d should have a name", i)
		assert.NotEmpty(t, crd.Spec.Group, "CRD %d should have a group", i)
		assert.Equal(t, "example.com", crd.Spec.Group, "CRD %d should have correct group", i)
	}
}

func TestCrds_VerifyExpectedCRDs(t *testing.T) {
	// Arrange
	crdsFolder := "./fixtures"

	// Act
	result, err := Load(crdsFolder, nil)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Verify expected CRD names are present
	expectedCRDs := []string{
		"test.example.com",
		"test2.example.com",
	}

	var foundCRDs []string
	for _, obj := range result {
		crd := obj.(*apiv1.CustomResourceDefinition)
		foundCRDs = append(foundCRDs, crd.Name)
	}

	for _, expectedCRD := range expectedCRDs {
		assert.Contains(t, foundCRDs, expectedCRD, "Should contain CRD: %s", expectedCRD)
	}
}

func TestCrds_VerifyKinds(t *testing.T) {
	// Arrange
	crdsFolder := "./fixtures"

	// Act
	result, err := Load(crdsFolder, nil)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Map of expected CRD names to their kinds
	expectedKinds := map[string]string{
		"test.example.com":  "Test",
		"test2.example.com": "Test2",
	}

	for _, obj := range result {
		crd := obj.(*apiv1.CustomResourceDefinition)
		expectedKind, exists := expectedKinds[crd.Name]
		require.True(t, exists, "CRD %s should be expected", crd.Name)
		assert.Equal(t, expectedKind, crd.Spec.Names.Kind, "CRD %s should have correct kind", crd.Name)
	}
}

func TestCrds_NonExistentFolder(t *testing.T) {
	// Arrange
	nonExistentFolder := "./non-existent-folder"

	// Act
	result, err := Load(nonExistentFolder, nil)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load CRDs from folder")
	assert.Contains(t, err.Error(), "folder does not exist")
	assert.Nil(t, result)
}

func TestCrds_EmptyFolder(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	// Act
	result, err := Load(tempDir, nil)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestMarshalBytesToCRDs_Success(t *testing.T) {
	// Arrange
	validCRD := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
spec:
  group: example.com
  names:
    kind: Test
    plural: tests
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
          status:
            type: object
`

	// Act
	result, err := marshalBytesToCRDs([]byte(validCRD), nil)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "test.example.com", result[0].Name)
	assert.Equal(t, "example.com", result[0].Spec.Group)
	assert.Equal(t, "Test", result[0].Spec.Names.Kind)
}

func TestMarshalBytesToCRDs_EmptyBytes(t *testing.T) {
	// Act
	result, err := marshalBytesToCRDs([]byte{}, nil)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty bytes passed for extracting CRD objects")
	assert.Nil(t, result)
}

func TestMarshalBytesToCRDs_NilBytes(t *testing.T) {
	// Act
	result, err := marshalBytesToCRDs(nil, nil)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty bytes passed for extracting CRD objects")
	assert.Nil(t, result)
}

func TestMarshalBytesToCRDs_InvalidYAML(t *testing.T) {
	// Arrange
	invalidYAML := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
spec:
  group: example.com
  names: [unclosed array
  scope: Namespaced
`

	// Act
	result, err := marshalBytesToCRDs([]byte(invalidYAML), nil)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode object")
	assert.Nil(t, result)
}

func TestMarshalBytesToCRDs_WrongKind(t *testing.T) {
	// Arrange
	wrongKind := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
data:
  key: value
`

	// Create a scheme with ConfigMap registered so we get to the validation logic
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Act
	result, err := marshalBytesToCRDs([]byte(wrongKind), scheme)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected CustomResourceDefinition, got")
	assert.Nil(t, result)
}

func TestMarshalBytesToCRDs_WrongAPIVersion(t *testing.T) {
	// Arrange
	wrongAPIVersion := `apiVersion: v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
spec:
  group: example.com
  names:
    kind: Test
    plural: tests
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

	// Act
	result, err := marshalBytesToCRDs([]byte(wrongAPIVersion), nil)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create object for GVK")
	assert.Nil(t, result)
}

func TestMarshalBytesToCRDs_MultipleCRDs(t *testing.T) {
	// Arrange
	multipleCRDs := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test1.example.com
spec:
  group: example.com
  names:
    kind: Test1
    plural: test1s
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
          status:
            type: object
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test2.example.com
spec:
  group: example.com
  names:
    kind: Test2
    plural: test2s
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
          status:
            type: object
`

	// Act
	result, err := marshalBytesToCRDs([]byte(multipleCRDs), nil)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 2)

	names := []string{result[0].Name, result[1].Name}
	assert.Contains(t, names, "test1.example.com")
	assert.Contains(t, names, "test2.example.com")
}

func TestMarshalBytesToCRDs_WithComments(t *testing.T) {
	// Arrange
	yamlWithComments := `# This is a comment
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
spec:
  group: example.com
  names:
    kind: Test
    plural: tests
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
          status:
            type: object
---
# Another comment
# More comments
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test2.example.com
spec:
  group: example.com
  names:
    kind: Test2
    plural: test2s
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
          status:
            type: object
`

	// Act
	result, err := marshalBytesToCRDs([]byte(yamlWithComments), nil)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 2)

	// Collect names safely, ensuring no nil objects
	var names []string
	for _, crd := range result {
		require.NotNil(t, crd, "CRD should not be nil")
		names = append(names, crd.Name)
	}

	assert.Contains(t, names, "test.example.com")
	assert.Contains(t, names, "test2.example.com")
}

func TestMarshalBytesToCRDs_OnlyComments(t *testing.T) {
	// Arrange
	onlyComments := `# This is a comment
# Another comment
# More comments
---
# Even more comments
`

	// Act
	result, err := marshalBytesToCRDs([]byte(onlyComments), nil)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 0)
}

func TestLoadFilesFromFolder_Success(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()
	testFiles := map[string]string{
		"file1.yaml": "content1",
		"file2.yaml": "content2",
		"file3.txt":  "content3",
	}

	for filename, content := range testFiles {
		err := os.WriteFile(filepath.Join(tempDir, filename), []byte(content), 0644)
		require.NoError(t, err)
	}

	// Act
	result, err := loadFilesFromFolder(tempDir)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 3)

	// Verify content is loaded
	var contents []string
	for _, fileBytes := range result {
		contents = append(contents, string(fileBytes))
	}
	assert.Contains(t, contents, "content1")
	assert.Contains(t, contents, "content2")
	assert.Contains(t, contents, "content3")
}

func TestLoadFilesFromFolder_NonExistentFolder(t *testing.T) {
	// Act
	result, err := loadFilesFromFolder("./non-existent-folder")

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "folder does not exist")
	assert.Nil(t, result)
}

func TestLoadFilesFromFolder_EmptyFolder(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	// Act
	result, err := loadFilesFromFolder(tempDir)

	// Assert
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestLoadFilesFromFolder_WithSubdirectories(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	// Create a file and a subdirectory
	err := os.WriteFile(filepath.Join(tempDir, "file1.yaml"), []byte("content1"), 0644)
	require.NoError(t, err)

	subDir := filepath.Join(tempDir, "subdir")
	err = os.Mkdir(subDir, 0755)
	require.NoError(t, err)

	err = os.WriteFile(filepath.Join(subDir, "file2.yaml"), []byte("content2"), 0644)
	require.NoError(t, err)

	// Act
	result, err := loadFilesFromFolder(tempDir)

	// Assert
	require.NoError(t, err)
	assert.Len(t, result, 2, "Should load files from both top-level directory and subdirectories")

	// Verify content is loaded from both locations
	var contents []string
	for _, fileBytes := range result {
		contents = append(contents, string(fileBytes))
	}
	assert.Contains(t, contents, "content1")
	assert.Contains(t, contents, "content2")
}

func TestLoadFilesFromFolder_FileReadError(t *testing.T) {
	// This test is more difficult to set up reliably across platforms
	// We'll skip it for now, but in a real scenario you might create
	// a file with no read permissions to test this case
	t.Skip("Skipping file read error test - platform dependent")
}

func TestCrds_WithInvalidCRDFiles(t *testing.T) {
	// Arrange
	tempDir := t.TempDir()

	// Create an invalid CRD file
	invalidCRD := `apiVersion:apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
spec:
  group: example.com
  names: [unclosed array
`
	err := os.WriteFile(filepath.Join(tempDir, "invalid.yaml"), []byte(invalidCRD), 0644)
	require.NoError(t, err)

	// Act
	result, err := Load(tempDir, nil)

	// Assert
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal bytes to CRDs")
	assert.Nil(t, result)
}

func TestWhitespaceBufferSize(t *testing.T) {
	// Test that the whitespace buffer size is reasonable
	assert.Equal(t, 4096, whitespaceBufferSize)
	assert.Greater(t, whitespaceBufferSize, 1024, "Buffer should be large enough for typical YAML documents")
}

func TestSchemeRegistration(t *testing.T) {
	// Test that scheme registration works correctly
	sch := runtime.NewScheme()
	err := apiv1.AddToScheme(sch)
	require.NoError(t, err)

	// Verify that CustomResourceDefinition is registered
	gvk := apiv1.SchemeGroupVersion.WithKind("CustomResourceDefinition")
	_, err = sch.New(gvk)
	assert.NoError(t, err, "CustomResourceDefinition should be registered in the scheme")
}

func TestYAMLDecoder_BufferSize(t *testing.T) {
	// Test that YAML decoder works with different buffer sizes
	testCRD := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
spec:
  group: example.com
  names:
    kind: Test
    plural: tests
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

	bufferSizes := []int{256, 1024, 4096, 8192}

	for _, bufferSize := range bufferSizes {
		t.Run(fmt.Sprintf("BufferSize_%d", bufferSize), func(t *testing.T) {
			dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(testCRD)), bufferSize)
			var crd apiv1.CustomResourceDefinition
			err := dec.Decode(&crd)
			assert.NoError(t, err, "Should decode successfully with buffer size %d", bufferSize)
			assert.Equal(t, "test.example.com", crd.Name)
		})
	}
}

// Integration tests using actual CRD files
func TestCrds_ActualCRDFiles(t *testing.T) {
	// Test with the actual CRD files in the project
	crdsFolder := "./crds"

	// Check if the CRDs folder exists (might not in all test environments)
	if _, err := os.Stat(crdsFolder); os.IsNotExist(err) {
		t.Skip("CRDs folder not found, skipping integration test")
	}

	// Act
	result, err := Load(crdsFolder, nil)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Verify all objects are valid CRDs
	for _, obj := range result {
		crd, ok := obj.(*apiv1.CustomResourceDefinition)
		require.True(t, ok, "Object should be a CustomResourceDefinition")
		assert.Equal(t, "egressgateway.kubernetes.azure.com", crd.Spec.Group)
		assert.Equal(t, "CustomResourceDefinition", crd.Kind)
		assert.Equal(t, "apiextensions.k8s.io/v1", crd.APIVersion)
	}
}

// Edge case tests
func TestYAMLDecoding_EdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		expectEmpty bool
	}{
		{
			name:        "Empty document",
			input:       "",
			expectError: true,
		},
		{
			name:        "Only whitespace",
			input:       "   \n\t\n   ",
			expectError: true,
		},
		{
			name:        "Only separators",
			input:       "---\n---\n---",
			expectError: false,
			expectEmpty: true,
		},
		{
			name:        "Mixed content with empty documents",
			input:       "---\n\n---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: test.example.com\nspec:\n  group: example.com\n  names:\n    kind: Test\n    plural: tests\n  scope: Namespaced\n  versions:\n  - name: v1\n    served: true\n    storage: true\n    schema:\n      openAPIV3Schema:\n        type: object\n---\n\n---",
			expectError: false,
			expectEmpty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := marshalBytesToCRDs([]byte(tt.input), nil)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.expectEmpty {
					assert.Empty(t, result)
				} else {
					assert.NotEmpty(t, result)
				}
			}
		})
	}
}
