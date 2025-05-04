package helmshim

import (
	"bytes"
	"testing"

	"github.com/Azure/eno/pkg/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsNullObject(t *testing.T) {
	cases := []struct {
		name string
		o    *unstructured.Unstructured
		want bool
	}{
		{
			name: "nil object",
			o:    nil,
			want: true,
		},
		{
			name: "empty object",
			o:    &unstructured.Unstructured{},
			want: true,
		},
		{
			name: "non-empty object",
			o: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
				},
			},
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isNullObject(c.o)
			assert.Equal(t, c.want, got)
		})
	}
}

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
