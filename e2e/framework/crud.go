package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

// SynthesizerOption configures optional fields on a Synthesizer.
type SynthesizerOption func(*apiv1.SynthesizerSpec)

// WithCommand sets the Synthesizer's command.
func WithCommand(cmd []string) SynthesizerOption {
	return func(s *apiv1.SynthesizerSpec) { s.Command = cmd }
}

// WithImage sets the Synthesizer's container image.
func WithImage(image string) SynthesizerOption {
	return func(s *apiv1.SynthesizerSpec) { s.Image = image }
}

// NewMinimalSynthesizer builds a Synthesizer with sensible defaults.
// Only the name is required; use WithImage and WithCommand to customise.
func NewMinimalSynthesizer(name string, opts ...SynthesizerOption) *apiv1.Synthesizer {
	spec := apiv1.SynthesizerSpec{
		Image: "docker.io/ubuntu:latest",
	}
	for _, o := range opts {
		o(&spec)
	}
	return &apiv1.Synthesizer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}
}

// CompositionOption configures optional fields on a Composition.
type CompositionOption func(*apiv1.CompositionSpec)

// WithSynthesizerRefs sets the Composition's synthesizer reference.
func WithSynthesizerRefs(ref apiv1.SynthesizerRef) CompositionOption {
	return func(s *apiv1.CompositionSpec) { s.Synthesizer = ref }
}

// NewComposition builds a Composition in the given namespace.
// Use WithSynthesizerRefs to bind it to a Synthesizer.
func NewComposition(name, ns string, opts ...CompositionOption) *apiv1.Composition {
	spec := apiv1.CompositionSpec{}
	for _, o := range opts {
		o(&spec)
	}
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: spec,
	}
}

// ToCommand converts Kubernetes objects into a bash command that echoes them as a
// KRM ResourceList on stdout — exactly what a synthesizer pod is expected to do.
// Each object must have its APIVersion and Kind set (e.g. corev1.ConfigMap with
// its TypeMeta populated, or an unstructured.Unstructured).
func ToCommand(objs ...client.Object) []string {
	items := make([]*unstructured.Unstructured, 0, len(objs))
	for _, obj := range objs {
		raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			panic(fmt.Sprintf("ToCommand: converting %s to unstructured: %v", obj.GetName(), err))
		}
		items = append(items, &unstructured.Unstructured{Object: raw})
	}

	rl := &krmv1.ResourceList{
		APIVersion: "config.kubernetes.io/v1",
		Kind:       "ResourceList",
		Items:      items,
	}

	data, err := json.Marshal(rl)
	if err != nil {
		panic(fmt.Sprintf("ToCommand: marshalling ResourceList: %v", err))
	}

	return []string{"/bin/bash", "-c", fmt.Sprintf("echo %q", string(data))}
}

// NewSymphony builds a Symphony with one variation per synthesizer name.
func NewSymphony(name, ns string, synthNames ...string) *apiv1.Symphony {
	variations := make([]apiv1.Variation, len(synthNames))
	for i, sn := range synthNames {
		variations[i] = apiv1.Variation{
			Synthesizer: apiv1.SynthesizerRef{Name: sn},
		}
	}
	return &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       apiv1.SymphonySpec{Variations: variations},
	}
}

// Cleanup deletes an object and waits for it to be gone.
func Cleanup(t *testing.T, ctx context.Context, cli client.Client, obj client.Object) {
	t.Helper()
	err := cli.Delete(ctx, obj)
	if apierrors.IsNotFound(err) {
		return
	}
	require.NoError(t, err, "failed to delete %s", obj.GetName())
	WaitForResourceDeleted(t, ctx, cli, obj, 60*time.Second)
}

// CreateStep returns a workflow step that creates the given object.
func CreateStep(t *testing.T, name string, cli client.Client, obj client.Object) flow.Steper {
	return flow.Func(name, func(ctx context.Context) error {
		t.Logf("creating %s", obj.GetName())
		return cli.Create(ctx, obj)
	})
}

// DeleteStep returns a workflow step that deletes the given object.
func DeleteStep(t *testing.T, name string, cli client.Client, obj client.Object) flow.Steper {
	return flow.Func(name, func(ctx context.Context) error {
		t.Logf("deleting %s", obj.GetName())
		return cli.Delete(ctx, obj)
	})
}

// CleanupStep returns a workflow step that cleans up the given objects.
func CleanupStep(t *testing.T, name string, cli client.Client, objs ...client.Object) flow.Steper {
	return flow.Func(name, func(ctx context.Context) error {
		for _, obj := range objs {
			Cleanup(t, ctx, cli, obj)
		}
		t.Logf("cleanup complete: %s", name)
		return nil
	})
}
