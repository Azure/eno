package mutation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
)

func TestPathExprManagedByEno(t *testing.T) {
	testCases := []struct {
		name          string
		path          string
		managedFields []metav1.ManagedFieldsEntry
		expected      bool
	}{
		{
			name: "FieldOwnedByEno",
			path: "self.data.foo",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "eno",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
			},
			expected: true,
		},
		{
			name: "FieldOwnedByEno_SingleQuotedIndex",
			path: "self.data['foo']",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "eno",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
			},
			expected: true,
		},
		{
			name: "FieldOwnedByEno_DoubleQuotedIndex",
			path: `self.data["foo"]`,
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "eno",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
			},
			expected: true,
		},
		{
			name: "FieldOwnedByOther",
			path: "self.data.foo",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "other-manager",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
			},
			expected: false,
		},
		{
			name: "MultipleManagers_EnoOwnsField",
			path: "self.data.foo",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "other-manager",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
				{
					Manager:   "eno",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
			},
			expected: true,
		},
		{
			name: "NestedPath",
			path: "self.spec.containers[0].image",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "eno",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSONWithIndex(t, "spec", "containers", 0, "image"),
					},
				},
			},
			expected: true,
		},
		{
			name:          "NoManagedFields",
			path:          "self.data.foo",
			managedFields: []metav1.ManagedFieldsEntry{},
			expected:      false,
		},
		{
			name: "NilPath",
			path: "",
			managedFields: []metav1.ManagedFieldsEntry{
				{
					Manager:   "eno",
					Operation: metav1.ManagedFieldsOperationApply,
					FieldsV1: &metav1.FieldsV1{
						Raw: createFieldSetJSON(t, "data", "foo"),
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "test-obj",
						"namespace": "default",
					},
					"data": map[string]interface{}{
						"foo": "bar",
					},
				},
			}
			obj.SetManagedFields(tc.managedFields)

			var pathExpr *PathExpr
			if tc.path != "" {
				var err error
				pathExpr, err = ParsePathExpr(tc.path)
				require.NoError(t, err)
			}

			owned := pathExpr.ManagedByEno(t.Context(), obj)
			assert.Equal(t, tc.expected, owned)
		})
	}
}

func createFieldSetJSON(t *testing.T, fieldNames ...string) []byte {
	fieldSet := &fieldpath.Set{}

	pathElements := make([]interface{}, len(fieldNames))
	for i, name := range fieldNames {
		fieldName := name
		pathElements[i] = fieldpath.PathElement{FieldName: &fieldName}
	}

	path, err := fieldpath.MakePath(pathElements...)
	require.NoError(t, err)

	fieldSet.Insert(path)

	jsonBytes, err := fieldSet.ToJSON()
	require.NoError(t, err)

	return jsonBytes
}

func createFieldSetJSONWithIndex(t *testing.T, fieldName1, fieldName2 string, index int, fieldName3 string) []byte {
	fieldSet := &fieldpath.Set{}

	field1 := fieldName1
	field2 := fieldName2
	field3 := fieldName3

	path, err := fieldpath.MakePath(
		fieldpath.PathElement{FieldName: &field1},
		fieldpath.PathElement{FieldName: &field2},
		fieldpath.PathElement{Index: &index},
		fieldpath.PathElement{FieldName: &field3},
	)
	require.NoError(t, err)

	fieldSet.Insert(path)

	jsonBytes, err := fieldSet.ToJSON()
	require.NoError(t, err)

	return jsonBytes
}
