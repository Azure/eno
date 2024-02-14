package helm

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testChartPath = "test-chart"
const testMultiDocChartPath = "test-chart-multi"

func TestRenderChart(t *testing.T) {
	tcs := []struct {
		name           string
		chart          string
		values         map[string]interface{}
		opts           []RenderOption
		expectedOutupt []*unstructured.Unstructured
		expectedError  error
	}{
		{
			name:          "invalid chart",
			chart:         "./some-invalid-path",
			expectedError: ErrChartNotFound,
		},
		{
			name:  "one document",
			chart: testChartPath,
			expectedOutupt: []*unstructured.Unstructured{
				newUnstructuredFromJSON(t,
					`{
						"kind": "ConfigMap",
						"apiVersion": "v1",
						"metadata": {
							"name": "cm-name"
						},
						"data": {
							"some": "value"
						}
				}`),
			},
			values: map[string]interface{}{
				"name": "cm-name",
			},
		},
		{
			name:  "two documents",
			chart: testMultiDocChartPath,
			expectedOutupt: []*unstructured.Unstructured{
				newUnstructuredFromJSON(t,
					`{
						"kind": "ConfigMap",
						"apiVersion": "v1",
						"metadata": {
							"name": "cm-name"
						},
						"data": {
							"some": "value"
						}
				}`),
				newUnstructuredFromJSON(t,
					`{
						"kind": "ConfigMap",
						"apiVersion": "v1",
						"metadata": {
							"name": "cm-name-2"
						},
						"data": {
							"some": "value"
						}
				}`),
			},
			values: map[string]interface{}{
				"name":  "cm-name",
				"name1": "cm-name-2",
			},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			_, currentFile, _, _ := runtime.Caller(0)
			cp := filepath.Join(filepath.Dir(currentFile), tc.chart)
			res, err := RenderChart(cp, tc.values, tc.opts...)
			if tc.expectedError != nil {
				require.ErrorIs(t, err, tc.expectedError)
			} else {
				require.ElementsMatch(t, res, tc.expectedOutupt)
			}
		})
	}
}

func newUnstructuredFromJSON(t *testing.T, j string) *unstructured.Unstructured {
	t.Helper()
	res := unstructured.Unstructured{}
	require.NoError(t, json.Unmarshal([]byte(j), &res))
	return &res
}
