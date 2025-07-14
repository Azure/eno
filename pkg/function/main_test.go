package function

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ExampleMain() {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	Main(fn)
	// Output: {"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"Pod","metadata":{"creationTimestamp":null,"name":"test-pod"},"spec":{"containers":null},"status":{}}]}
}

func ExampleInputs() {
	type exampleInputs struct {
		MySecret *corev1.Secret `eno_key:"test-secret"`
	}

	fn := func(inputs exampleInputs) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = string(inputs.MySecret.Data["key"])
		return []client.Object{output}, nil
	}

	ir := newTestInputReader()
	main(fn, ir, NewDefaultOutputWriter())
	// Output: {"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"Pod","metadata":{"creationTimestamp":null,"name":"foobar\n"},"spec":{"containers":null},"status":{}}]}
}

func ExampleAddCustomInputType() {
	type myType struct {
		SecretKey string
	}

	AddCustomInputType(func(in *corev1.Secret) (*myType, error) {
		return &myType{
			SecretKey: string(in.Data["key"]),
		}, nil
	})

	type exampleInputs struct {
		CustomInput *myType `eno_key:"test-secret"`
	}

	fn := func(inputs exampleInputs) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = string(inputs.CustomInput.SecretKey)
		return []client.Object{output}, nil
	}

	ir := newTestInputReader()
	main(fn, ir, NewDefaultOutputWriter())
	// Output: {"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"Pod","metadata":{"creationTimestamp":null,"name":"foobar\n"},"spec":{"containers":null},"status":{}}]}
}

func ExampleAddCustomInputType_slice() {
	type myType struct {
		SecretKey string
	}

	AddCustomInputType(func(in *corev1.Secret) ([]*myType, error) {
		return []*myType{{
			SecretKey: string(in.Data["key"]),
		}}, nil
	})

	type exampleInputs struct {
		CustomInputs []*myType `eno_key:"test-secret"`
	}

	fn := func(inputs exampleInputs) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = string(inputs.CustomInputs[0].SecretKey)
		return []client.Object{output}, nil
	}

	ir := newTestInputReader()
	main(fn, ir, NewDefaultOutputWriter())
	// Output: {"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"Pod","metadata":{"creationTimestamp":null,"name":"foobar\n"},"spec":{"containers":null},"status":{}}]}
}

func TestMain(t *testing.T) {
	outBuf := &bytes.Buffer{}
	ow := NewOutputWriter(outBuf, nil)
	ir := newTestInputReader()

	fn := func(inputs testSimpleInputs) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		output.Annotations = map[string]string{
			"cm-value":     inputs.MyConfigmap.Data["key"],
			"secret-value": string(inputs.MySecret.Data["key"]),
		}
		return []client.Object{output}, nil
	}

	require.NoError(t, main(fn, ir, ow))
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[{\"apiVersion\":\"v1\",\"kind\":\"Pod\",\"metadata\":{\"annotations\":{\"cm-value\":\"foo\",\"secret-value\":\"foobar\\n\"},\"creationTimestamp\":null,\"name\":\"test-pod\"},\"spec\":{\"containers\":null},\"status\":{}}]}\n", outBuf.String())
}

func TestMainInputMissing(t *testing.T) {
	outBuf := &bytes.Buffer{}
	inBuf := bytes.NewBufferString(`{}`)

	ow := NewOutputWriter(outBuf, nil)
	ir, err := NewInputReader(inBuf)
	require.NoError(t, err)

	fn := func(inputs testSimpleInputs) ([]client.Object, error) {
		output := &corev1.Pod{}
		return []client.Object{output}, nil
	}

	require.NoError(t, main(fn, ir, ow))
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[],\"results\":[{\"message\":\"error while reading input with key \\\"test-cm\\\": input \\\"test-cm\\\" was not found\",\"severity\":\"error\"}]}\n", outBuf.String())
}

func TestMainError(t *testing.T) {
	outBuf := &bytes.Buffer{}
	inBuf := bytes.NewBufferString(`{"items": [{"kind": "ConfigMap", "apiVersion": "v1", "metadata": {"name": "test-configmap", "annotations": {"eno.azure.io/input-key": "test-cm"}}, "data": {"key": "foo"}}, {"kind": "Secret", "apiVersion": "v1", "metadata": {"name": "test-secret", "annotations": {"eno.azure.io/input-key": "test-secret"}}, "data": {"key": "Zm9vYmFyCg=="}}]}`)

	ow := NewOutputWriter(outBuf, nil)
	ir, err := NewInputReader(inBuf)
	require.NoError(t, err)

	fn := func(inputs testSimpleInputs) ([]client.Object, error) {
		return []client.Object{}, fmt.Errorf("foobar")
	}

	require.NoError(t, main(fn, ir, ow))
	assert.Equal(t, "{\"apiVersion\":\"config.kubernetes.io/v1\",\"kind\":\"ResourceList\",\"items\":[],\"results\":[{\"message\":\"foobar\",\"severity\":\"error\"}]}\n", outBuf.String())
}

func ExampleMain_withMungers() {
	// Example using precreted mungers
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	//stdout of main will be compared with output comment below becausse this is an example
	Main(fn, WithManagedByEno(), WithReconcilationInterval(time.Minute))
	// Output: {"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"Pod","metadata":{"annotations":{"eno.azure.io/reconcile-interval":"1m0s"},"creationTimestamp":null,"labels":{"app.kubernetes.io/managed-by":"Eno"},"name":"test-pod"},"spec":{"containers":null},"status":{}}]}
}

type testSimpleInputs struct {
	MyConfigmap *corev1.ConfigMap `eno_key:"test-cm"`
	MySecret    *corev1.Secret    `eno_key:"test-secret"`
}

func newTestInputReader() *InputReader {
	inBuf := bytes.NewBufferString(`{"items": [{"kind": "ConfigMap", "apiVersion": "v1", "metadata": {"name": "test-configmap", "annotations": {"eno.azure.io/input-key": "test-cm"}}, "data": {"key": "foo"}}, {"kind": "Secret", "apiVersion": "v1", "metadata": {"name": "test-secret", "annotations": {"eno.azure.io/input-key": "test-secret"}}, "data": {"key": "Zm9vYmFyCg=="}}]}`)
	ir, err := NewInputReader(inBuf)
	if err != nil {
		panic(err)
	}
	return ir
}

// Test inputs that implement MungerError
type mungerInputs struct {
	MyConfigmap *corev1.ConfigMap `eno_key:"test-cm"`
	munged      bool
}

// Implement MungerError interface
func (m *mungerInputs) Munge() error {
	m.munged = true
	// Modify the configmap data as part of munging
	if m.MyConfigmap != nil {
		m.MyConfigmap.Data["munged"] = "true"
	}
	return nil
}

func TestMungerError(t *testing.T) {
	outBuf := &bytes.Buffer{}
	ow := NewOutputWriter(outBuf, nil)
	ir := newTestInputReader()

	fn := func(inputs mungerInputs) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		output.Annotations = map[string]string{
			"munged":           fmt.Sprintf("%v", inputs.munged),
			"configmap-munged": inputs.MyConfigmap.Data["munged"],
		}
		return []client.Object{output}, nil
	}

	require.NoError(t, main(fn, ir, ow))

	// Verify that munging occurred
	output := outBuf.String()
	assert.Contains(t, output, `"munged":"true"`)
	assert.Contains(t, output, `"configmap-munged":"true"`)
}

// Test inputs that implement MungerError and return an error
type failingMungerInputs struct {
	MyConfigmap *corev1.ConfigMap `eno_key:"test-cm"`
}

// Implement MungerError interface that returns an error
func (m *failingMungerInputs) Munge() error {
	return fmt.Errorf("munging failed")
}

func TestMungerErrorFailure(t *testing.T) {
	outBuf := &bytes.Buffer{}
	ow := NewOutputWriter(outBuf, nil)
	ir := newTestInputReader()

	fn := func(inputs failingMungerInputs) ([]client.Object, error) {
		// This should not be called because munging will fail
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	err := main(fn, ir, ow)

	// Verify that the error from Munge() is returned
	require.Error(t, err)
	assert.Contains(t, err.Error(), "munging failed")
}
