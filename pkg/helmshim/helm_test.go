package helmshim

import (
	"bytes"
	"testing"

	"github.com/Azure/eno/pkg/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderChart(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output)

	input := bytes.NewBufferString(`{ "items": [{ "apiVersion": "v1", "kind": "ConfigMap", "metadata": { "name": "test-cm", "annotations": { "eno.azure.io/input-key": "foo" } } }] }`)
	i, err := function.NewInputReader(input)
	require.NoError(t, err)

	err = renderChart(i, o, WithChartPath("fixtures/basic-chart"))
	require.NoError(t, err)
	require.NoError(t, o.Write())
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"input\":\"{\\\"apiVersion\\\":\\\"v1\\\",\\\"kind\\\":\\\"ConfigMap\\\",\\\"metadata\\\":{\\\"annotations\\\":{\\\"eno.azure.io/input-key\\\":\\\"foo\\\"},\\\"name\\\":\\\"test-cm\\\"}}\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":null}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output.String())
}

func TestRenderChartWithCustomValues(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output)
	i, err := function.NewInputReader(bytes.NewBufferString("{}"))
	require.NoError(t, err)

	err = renderChart(i, o, WithChartPath("fixtures/basic-chart"), WithValuesFunc(func(ir *function.InputReader) (map[string]any, error) {
		return map[string]any{"name": "my-test-cm"}, nil
	}))
	require.NoError(t, err)
	require.NoError(t, o.Write())
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"input\":\"null\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"my-test-cm\"}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output.String())
}
