package execution

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestExecHandlerTimeout(t *testing.T) {
	handle := NewExecHandler()

	syn := &apiv1.Synthesizer{}
	syn.Spec.Command = []string{"/bin/sh", "-c", "sleep 1"}
	syn.Spec.ExecTimeout = &metav1.Duration{Duration: time.Millisecond}
	rl := &krmv1.ResourceList{}

	_, err := handle(context.Background(), syn, rl)
	require.EqualError(t, err, "signal: killed")
}

func TestExecHandlerEmpty(t *testing.T) {
	handle := NewExecHandler()

	syn := &apiv1.Synthesizer{}
	rl := &krmv1.ResourceList{}

	_, err := handle(context.Background(), syn, rl)
	require.EqualError(t, err, "exec: \"synthesize\": executable file not found in $PATH")
}
