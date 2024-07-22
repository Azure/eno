package helmshim

import (
	"bytes"
	"testing"

	"github.com/Azure/eno/pkg/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRenderChart(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output)

	input := bytes.NewBufferString(`{ "items": [{ "apiVersion": "v1", "kind": "ConfigMap", "metadata": { "name": "test-cm", "annotations": { "eno.azure.io/input-key": "foo" } } }] }`)
	i, err := function.NewInputReader(input)
	require.NoError(t, err)

	err = RenderChart(WithInputReader(i), WithOutputWriter(o), WithChartPath("fixtures/basic-chart"))
	require.NoError(t, err)
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"input\":\"{\\\"apiVersion\\\":\\\"v1\\\",\\\"kind\\\":\\\"ConfigMap\\\",\\\"metadata\\\":{\\\"annotations\\\":{\\\"eno.azure.io/input-key\\\":\\\"foo\\\"},\\\"name\\\":\\\"test-cm\\\"}}\",\"inputResourceName\":\"test-cm\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":null}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output.String())
}

func TestRenderChartWithCustomValues(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output)
	i, err := function.NewInputReader(bytes.NewBufferString("{}"))
	require.NoError(t, err)

	err = RenderChart(
		WithChartPath("fixtures/basic-chart"),
		WithInputReader(i),
		WithOutputWriter(o),
		WithMungeFunc(func(u *unstructured.Unstructured) {
			assert.Contains(t, []string{"my-test-cm", "foo"}, u.GetName())
			if u.GetName() == "my-test-cm" {
				unstructured.SetNestedField(u.Object, "set by munge func", "data", "another-value")
			}
		}),
		WithValuesFunc(func(ir *function.InputReader) (map[string]any, error) {
			return map[string]any{"name": "my-test-cm"}, nil
		}))
	require.NoError(t, err)
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"another-value\":\"set by munge func\",\"input\":\"null\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"my-test-cm\"}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output.String())
}
