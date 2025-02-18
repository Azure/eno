package helmshim

import (
	"bytes"
	"os"
	"testing"

	"github.com/Azure/eno/pkg/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderChart(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output, nil)

	input := bytes.NewBufferString(`{ "items": [{ "apiVersion": "v1", "kind": "ConfigMap", "metadata": { "name": "test-cm", "annotations": { "eno.azure.io/input-key": "foo" } } }] }`)
	i, err := function.NewInputReader(input)
	require.NoError(t, err)
	err = RenderChart(WithInputReader(i), WithOutputWriter(o), WithChartPath("fixtures/basic-chart"))
	require.NoError(t, err)
	err = o.Write()
	require.NoError(t, err)
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"input\":\"{\\\"apiVersion\\\":\\\"v1\\\",\\\"kind\\\":\\\"ConfigMap\\\",\\\"metadata\\\":{\\\"annotations\\\":{\\\"eno.azure.io/input-key\\\":\\\"foo\\\"},\\\"name\\\":\\\"test-cm\\\"}}\",\"inputResourceName\":\"test-cm\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":null}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output.String())
}

func TestRenderChartWithDefaultOutputWriter(t *testing.T) {
	// Save the original stdout and redirect it to a pipe.
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	input := bytes.NewBufferString(`{ "items": [{ "apiVersion": "v1", "kind": "ConfigMap", "metadata": { "name": "test-cm", "annotations": { "eno.azure.io/input-key": "foo" } } }] }`)
	i, err := function.NewInputReader(input)
	require.NoError(t, err)
	// Do not provide an output writer and use the default output writer.
	err = RenderChart(WithInputReader(i), WithChartPath("fixtures/basic-chart"))
	require.NoError(t, err)

	// Close the writer and capture the output from reader.
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()
	// Restore the original stdout.
	os.Stdout = oldStdout

	// Check the output.
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"input\":\"{\\\"apiVersion\\\":\\\"v1\\\",\\\"kind\\\":\\\"ConfigMap\\\",\\\"metadata\\\":{\\\"annotations\\\":{\\\"eno.azure.io/input-key\\\":\\\"foo\\\"},\\\"name\\\":\\\"test-cm\\\"}}\",\"inputResourceName\":\"test-cm\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":null}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output)
}

func TestRenderChartWithCustomValues(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output, nil)
	i, err := function.NewInputReader(bytes.NewBufferString("{}"))
	require.NoError(t, err)

	err = RenderChart(
		WithChartPath("fixtures/basic-chart"),
		WithInputReader(i),
		WithOutputWriter(o),
		WithValuesFunc(func(ir *function.InputReader) (map[string]any, error) {
			return map[string]any{"name": "my-test-cm"}, nil
		}))
	require.NoError(t, err)
	err = o.Write()
	require.NoError(t, err)
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"data\":{\"input\":\"null\",\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"name\":\"my-test-cm\"}},{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}}]}\n", output.String())
}

func TestRenderChartWithHelmHook(t *testing.T) {
	output := bytes.NewBuffer(nil)
	o := function.NewOutputWriter(output, nil)
	i, err := function.NewInputReader(bytes.NewBufferString("{}"))
	require.NoError(t, err)

	err = RenderChart(
		WithChartPath("fixtures/hook-chart"),
		WithInputReader(i),
		WithOutputWriter(o),
		WithValuesFunc(func(ir *function.InputReader) (map[string]any, error) {
			return map[string]any{"name": "my-test-cm"}, nil
		}))
	require.NoError(t, err)
	err = o.Write()
	require.NoError(t, err)
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"somegroup.io/v9001\",\"kind\":\"ATypeNotKnownByTheScheme\",\"metadata\":{\"name\":\"foo\"}},{\"apiVersion\":\"v1\",\"data\":{\"some\":\"value\"},\"kind\":\"ConfigMap\",\"metadata\":{\"annotations\":{\"helm.sh/hook\":\"post-install,post-upgrade\",\"helm.sh/hook-delete-policy\":\"before-hook-creation\",\"helm.sh/hook-weight\":\"1\"},\"name\":\"my-test-cm\"}}]}\n", output.String())
}
