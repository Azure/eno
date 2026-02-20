package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// NewMinimalSynthesizer builds a Synthesizer that outputs a ConfigMap with the given data.
// It uses ubuntu:latest with a bash command to echo a KRM ResourceList.
func NewMinimalSynthesizer(name, cmName, key, value string) *apiv1.Synthesizer {
	return &apiv1.Synthesizer{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiv1.SynthesizerSpec{
			Image: "docker.io/ubuntu:latest",
			Command: []string{
				"/bin/bash", "-c",
				fmt.Sprintf(`echo '{"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s","namespace":"default"},"data":{"%s":"%s"}}]}'`, cmName, key, value),
			},
		},
	}
}

// NewComposition builds a Composition referencing a synthesizer by name.
func NewComposition(name, ns, synthName string) *apiv1.Composition {
	return &apiv1.Composition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: apiv1.CompositionSpec{
			Synthesizer: apiv1.SynthesizerRef{
				Name: synthName,
			},
		},
	}
}

// ConfigMap returns a ConfigMap object reference for use with wait helpers.
func ConfigMap(name, ns string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
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
	WaitForResourceGone(t, ctx, cli, obj, 60*time.Second)
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
