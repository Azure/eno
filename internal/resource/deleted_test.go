package resource

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeletedMethod(t *testing.T) {
	// Create json patches for testing
	patchWithDeletionTimestamp, err := jsonpatch.DecodePatch([]byte(`[{"op": "add", "path": "/metadata/deletionTimestamp", "value": "2024-01-22T19:13:15Z"}]`))
	require.NoError(t, err)
	
	patchWithoutDeletionTimestamp, err := jsonpatch.DecodePatch([]byte(`[{"op": "add", "path": "/data/foo", "value": "bar"}]`))
	require.NoError(t, err)

	// Define test cases for the Deleted method
	testCases := []struct {
		name                   string
		resource               Resource
		compositionAnnotations map[string]string
		deletionTimestamp      *metav1.Time
		expected               bool
	}{
		{
			name:                   "Regular resource with composition not being deleted",
			resource:               Resource{},
			compositionAnnotations: nil,
			deletionTimestamp:      nil,
			expected:               false,
		},
		{
			name:                   "Regular resource with composition being deleted without orphaning",
			resource:               Resource{},
			compositionAnnotations: nil,
			deletionTimestamp:      &metav1.Time{},
			expected:               true,
		},
		{
			name:                   "Regular resource with composition being deleted with orphaning",
			resource:               Resource{},
			compositionAnnotations: map[string]string{"eno.azure.io/deletion-strategy": "orphan"},
			deletionTimestamp:      &metav1.Time{},
			expected:               false,
		},
		{
			name:                   "Manifest deleted resource",
			resource:               Resource{ManifestDeleted: true},
			compositionAnnotations: nil,
			deletionTimestamp:      nil,
			expected:               true,
		},
		{
			name: "Patch with deletion timestamp with composition not being deleted",
			resource: Resource{
				Patch: patchWithDeletionTimestamp,
			},
			compositionAnnotations: nil,
			deletionTimestamp:      nil,
			expected:               false, // Patch deletion only happens when composition is being deleted
		},
		{
			name: "Patch with deletion timestamp with composition being deleted with orphaning",
			resource: Resource{
				Patch: patchWithDeletionTimestamp,
			},
			compositionAnnotations: map[string]string{"eno.azure.io/deletion-strategy": "orphan"},
			deletionTimestamp:      &metav1.Time{},
			expected:               true, // Patch deletion happens when composition is being deleted, regardless of orphaning
		},
		{
			name: "Patch without deletion timestamp with composition being deleted with orphaning",
			resource: Resource{
				Patch: patchWithoutDeletionTimestamp,
			},
			compositionAnnotations: map[string]string{"eno.azure.io/deletion-strategy": "orphan"},
			deletionTimestamp:      &metav1.Time{},
			expected:               false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			comp := &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Annotations:       tc.compositionAnnotations,
					DeletionTimestamp: tc.deletionTimestamp,
				},
			}
			actual := tc.resource.Deleted(comp)
			assert.Equal(t, tc.expected, actual)
		})
	}
}