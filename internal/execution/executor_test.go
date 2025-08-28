package execution

import (
	"context"
	"slices"
	"strings"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{
				Items:   []*unstructured.Unstructured{out},
				Results: []*krmv1.Result{{Message: "foo", Severity: "warning"}},
			}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
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
		Resource: apiv1.ResourceRef{Kind: "ConfigMap", Version: "v1"},
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			require.Len(t, rl.Items, 1)
			assert.Equal(t, "ConfigMap", rl.Items[0].GetKind())
			assert.Equal(t, "test-input", rl.Items[0].GetName())
			assert.Equal(t, map[string]string{"eno.azure.io/input-key": "foo"}, rl.Items[0].GetAnnotations())

			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{Items: []*unstructured.Unstructured{out}}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Synthesized)
}

func TestWithImplicitBindingInputs(t *testing.T) {
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
		Key: "foo",
		Resource: apiv1.ResourceRef{
			Kind:      "ConfigMap",
			Version:   "v1",
			Name:      input.Name,
			Namespace: input.Namespace,
		},
	}}
	err = cli.Create(ctx, syn)
	require.NoError(t, err)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	err = cli.Create(ctx, comp)
	require.NoError(t, err)

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			require.Len(t, rl.Items, 1)
			assert.Equal(t, "ConfigMap", rl.Items[0].GetKind())
			assert.Equal(t, "test-input", rl.Items[0].GetName())
			assert.Equal(t, map[string]string{"eno.azure.io/input-key": "foo"}, rl.Items[0].GetAnnotations())

			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{Items: []*unstructured.Unstructured{out}}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.NotNil(t, comp.Status.CurrentSynthesis.Synthesized)
}

func TestWithVersionedInput(t *testing.T) {
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
	input.Annotations = map[string]string{"eno.azure.io/revision": "123"}
	err := cli.Create(ctx, input)
	require.NoError(t, err)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-synth"
	syn.Spec.Refs = []apiv1.Ref{{
		Key:      "foo",
		Resource: apiv1.ResourceRef{Kind: "ConfigMap", Group: "", Version: "v1"},
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{Items: []*unstructured.Unstructured{out}}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	require.Len(t, comp.Status.CurrentSynthesis.InputRevisions, 1)
	assert.Equal(t, 123, *comp.Status.CurrentSynthesis.InputRevisions[0].Revision)
}

func TestUUIDMismatch(t *testing.T) {
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
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{
				Items:   []*unstructured.Unstructured{out},
				Results: []*krmv1.Result{{Message: "foo", Severity: "warning"}},
			}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        "a new uuid",
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.Nil(t, comp.Status.CurrentSynthesis.Synthesized)
}

func TestSynthesisCanceled(t *testing.T) {
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid", Canceled: ptr.To(metav1.Now())}
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
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{
				Items:   []*unstructured.Unstructured{out},
				Results: []*krmv1.Result{{Message: "foo", Severity: "warning"}},
			}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.Nil(t, comp.Status.CurrentSynthesis)
}

func TestCompletionMismatchDuringSynthesis(t *testing.T) {
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}
	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			// Act as if another synthesizer pod with the same synthesis uuid but different attempt has updated the status concurrently
			err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
				require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
				comp.Status.InFlightSynthesis.UUID = "not-the-original"
				return cli.Status().Update(ctx, comp)
			})
			require.NoError(t, err)

			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{
				Items:   []*unstructured.Unstructured{out},
				Results: []*krmv1.Result{{Message: "foo", Severity: "warning"}},
			}, nil
		},
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.Nil(t, comp.Status.CurrentSynthesis)
	assert.Equal(t, "not-the-original", comp.Status.InFlightSynthesis.UUID)
}

// TestDeleteResource verifies any resources that were previously created
// but are no longer included in the executor's output will be deleted.
func TestDeleteResource(t *testing.T) {
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}
	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			out2 := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]any{
						"name":      "test2",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
				},
			}
			return &krmv1.ResourceList{
				Items:   []*unstructured.Unstructured{out, out2},
				Results: []*krmv1.Result{{Message: "foo", Severity: "warning"}},
			}, nil
		},
	}

	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	// remove test2 configmap
	e.Handler = func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		out := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test",
					"namespace": "default",
				},
				"data": map[string]any{"foo": "bar"},
			},
		}
		return &krmv1.ResourceList{
			Items:   []*unstructured.Unstructured{out},
			Results: []*krmv1.Result{{Message: "foo", Severity: "warning"}},
		}, nil
	}

	// Set InFlightSynthesis for the second synthesis
	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid-2"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	// Resynthesize with only one configmap in output.
	env.SynthesisUUID = comp.Status.InFlightSynthesis.UUID
	err = e.Synthesize(ctx, env)
	require.NoError(t, err)

	// Get current synthesis's resource slice
	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	assert.NoError(t, err)
	rs := &apiv1.ResourceSlice{}
	rs.Name = comp.Status.CurrentSynthesis.ResourceSlices[0].Name
	rs.Namespace = comp.Namespace
	err = cli.Get(ctx, client.ObjectKeyFromObject(rs), rs)
	assert.NoError(t, err)

	// Check resource slice and should find resource removed from output is marked as deleted
	deletedIdx := slices.IndexFunc(rs.Spec.Resources, func(resource apiv1.Manifest) bool {
		return strings.Contains(resource.Manifest, "test2")
	})

	assert.NotEqual(t, -1, deletedIdx)
	assert.Equal(t, rs.Spec.Resources[deletedIdx].Deleted, true)
}

func TestError(t *testing.T) {
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
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
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
					"data": map[string]any{"foo": "bar"},
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
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.Error(t, err)

	err = e.Synthesize(ctx, env)
	require.Error(t, err)

	err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
	require.NoError(t, err)
	assert.Nil(t, comp.Status.CurrentSynthesis)
	assert.NotNil(t, comp.Status.InFlightSynthesis.Synthesized)
	assert.Len(t, comp.Status.InFlightSynthesis.ResourceSlices, 0)
	require.Len(t, comp.Status.InFlightSynthesis.Results, 1)
	assert.Equal(t, "foo", comp.Status.InFlightSynthesis.Results[0].Message)
}

func TestInvalidResource(t *testing.T) {
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

	comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
	err = cli.Status().Update(ctx, comp)
	require.NoError(t, err)

	e := &Executor{
		Reader: cli,
		Writer: cli,
		Handler: func(ctx context.Context, s *apiv1.Synthesizer, rl *krmv1.ResourceList) (*krmv1.ResourceList, error) {
			out := &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "", // missing
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "default",
					},
				},
			}
			return &krmv1.ResourceList{Items: []*unstructured.Unstructured{out}}, nil
		},
	}
	env := &Env{
		CompositionName:      comp.Name,
		CompositionNamespace: comp.Namespace,
		SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
	}

	err = e.Synthesize(ctx, env)
	require.Error(t, err)
}

func TestExecErrors(t *testing.T) {
	tests := []struct {
		Name          string
		Command       []string
		ExpectedError string
	}{
		{
			Name:          "invalid json",
			Command:       []string{"/bin/sh", "-c", "echo 'Invalid JSON'"},
			ExpectedError: "Synthesizer error: the synthesizer process wrote invalid json to stdout",
		},
		{
			Name:          "missing command",
			Command:       []string{"not-a-real-command"},
			ExpectedError: `Synthesizer error: exec: "not-a-real-command": executable file not found in $PATH (likely a mismatch between the Synthesizer object and container image)`,
		},
		{
			Name:          "exit 2",
			Command:       []string{"/bin/sh", "-c", "exit 2"},
			ExpectedError: `Synthesizer error: exit status 2 (see synthesis pod logs for more details)`,
		},
	}

	for _, test := range tests {
		ctx := context.Background()
		scheme := runtime.NewScheme()
		require.NoError(t, apiv1.SchemeBuilder.AddToScheme(scheme))

		cli := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&apiv1.ResourceSlice{}, &apiv1.Composition{}).
			Build()

		syn := &apiv1.Synthesizer{}
		syn.Name = "test-synth"
		syn.Spec.Command = test.Command
		err := cli.Create(ctx, syn)
		require.NoError(t, err)

		comp := &apiv1.Composition{}
		comp.Name = "test-comp"
		comp.Namespace = "default"
		comp.Spec.Synthesizer.Name = syn.Name
		err = cli.Create(ctx, comp)
		require.NoError(t, err)

		comp.Status.InFlightSynthesis = &apiv1.Synthesis{UUID: "test-uuid"}
		err = cli.Status().Update(ctx, comp)
		require.NoError(t, err)

		e := &Executor{Reader: cli, Writer: cli, Handler: NewExecHandler()}
		env := &Env{
			CompositionName:      comp.Name,
			CompositionNamespace: comp.Namespace,
			SynthesisUUID:        comp.Status.InFlightSynthesis.UUID,
		}

		err = e.Synthesize(ctx, env)
		require.Error(t, err)

		err = cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		require.NoError(t, err)
		require.NotNil(t, comp.Status.InFlightSynthesis)
		assert.Len(t, comp.Status.InFlightSynthesis.ResourceSlices, 0)
		require.Len(t, comp.Status.InFlightSynthesis.Results, 1)
		assert.Equal(t, krmv1.ResultSeverityError, comp.Status.InFlightSynthesis.Results[0].Severity)
		assert.Equal(t, test.ExpectedError, comp.Status.InFlightSynthesis.Results[0].Message)
	}
}
