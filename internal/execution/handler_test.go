package execution

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestExecHandler(t *testing.T) {
	handle := NewExecHandler()

	syn := &apiv1.Synthesizer{}
	syn.Spec.Command = []string{"/bin/sh", "-c", "cat /dev/stdin > /dev/stdout"}
	rl := &krmv1.ResourceList{Items: []*unstructured.Unstructured{{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]string{
				"name":      "test",
				"namespace": "default",
			},
			"data": map[string]string{"foo": "bar"},
		},
	}}}

	out, err := handle(context.Background(), syn, rl)
	require.NoError(t, err)
	require.Len(t, out.Items, 1)
}

func TestExecHandlerEmpty(t *testing.T) {
	handle := NewExecHandler()

	syn := &apiv1.Synthesizer{}
	rl := &krmv1.ResourceList{}

	_, err := handle(context.Background(), syn, rl)
	require.EqualError(t, err, "executing command: exec: \"synthesize\": executable file not found in $PATH")
}

func TestExecHandlerInvalidJSON(t *testing.T) {
	handle := NewExecHandler()

	syn := &apiv1.Synthesizer{}
	syn.Spec.Command = []string{"/bin/sh", "-c", "echo 'Invalid JSON' > /dev/stdout"}
	rl := &krmv1.ResourceList{}
	_, err := handle(context.Background(), syn, rl)
	require.EqualError(t, err, `error while parsing synthesizer's stdout as json "invalid character 'I' looking for beginning of value" - raw output: Invalid JSON`+"\n")
}
