package execution

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBasics(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&apiv1.ResourceSlice{}, &apiv1.Composition{}).
		Build()

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-synth"
	err := cli.Create(ctx, syn)
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	err = cli.Create(ctx, comp)
	require.NoError(t, err)

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]string{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]string{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{
				Items:   []*unstructured.Unstructured{out},
				Results: []*krmv1.Result{{Message: "foo", Severity: "error"}},
			}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.CurrentSynthesis.UUID,
	}

	// First pass
	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Synthesized)
	assert.Len(t, comp.Status.CurrentSynthesis.ResourceSlices, 1)
	require.Len(t, comp.Status.CurrentSynthesis.Results, 1)
	assert.Equal(t, "foo", comp.Status.CurrentSynthesis.Results[0].Message)

	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err = cli.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		require.NoError(t, err)
		assert.Len(t, slice.Spec.Resources, 1)
	}

	// Second pass
	comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid-2"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	env.SynthesisUUID = "test-uuid-2"
	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	// No-op since the synthesis is already complete
	err = e.Synthesize(ctx, env)
	require.NoError(t, err)
}

func TestWithInputs(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))
	require.NoError(t, corev1.SchemeBuilder.AddToScheme(scheme))

	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&apiv1.ResourceSlice{}, &apiv1.Composition{}).
		Build()

	input := &corev1.ConfigMap{}
	input.Name = "test-input"
	input.Namespace = "default"
	err := cli.Create(ctx, input)
	require.NoError(t, err)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-synth"
	syn.Spec.Refs = []apiv1.Ref{{
		Key:      "foo",
		Resource: apiv1.ResourceRef{Kind: "ConfigMap"},
	}}
	err = cli.Create(ctx, syn)
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Bindings = []apiv1.Binding{{
		Key: "foo",
		Resource: apiv1.ResourceBinding{
			Name:      input.Name,
			Namespace: input.Namespace,
		},
	}}
	comp.Spec.Synthesizer.Name = syn.Name
	err = cli.Create(ctx, comp)
	require.NoError(t, err)

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			require.Len(t, rl.Items, 1)
			// TODO: Assert

			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]string{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]string{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{Items: []*unstructured.Unstructured{out}}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.CurrentSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Synthesized)
}

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
