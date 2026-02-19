package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

func init() {
	err := apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	if err != nil {
		panic(fmt.Sprintf("failed to add eno scheme: %v", err))
	}
}

// newClient creates a controller-runtime client using the in-cluster or KUBECONFIG config.
func newClient(t *testing.T) client.Client {
	t.Helper()
	cfg, err := ctrl.GetConfig()
	require.NoError(t, err, "failed to get kubeconfig — is KUBECONFIG set?")

	cli, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	require.NoError(t, err, "failed to create client")
	return cli
}

// uniqueName generates a test-unique resource name with a timestamp suffix.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}

// waitForCompositionReady polls until the composition's Simplified.Status equals "Ready".
func waitForCompositionReady(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		comp := &apiv1.Composition{}
		if err := cli.Get(ctx, key, comp); err != nil {
			return false, nil
		}
		if comp.Status.Simplified == nil {
			return false, nil
		}
		t.Logf("composition %s status: %s", key.Name, comp.Status.Simplified.String())
		return comp.Status.Simplified.Status == "Ready", nil
	})
	require.NoError(t, err, "timed out waiting for composition %s to become Ready", key.Name)
}

// waitForCompositionResynthesized polls until the composition's ObservedSynthesizerGeneration
// advances beyond minGen AND status returns to "Ready".
func waitForCompositionResynthesized(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, minGen int64, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		comp := &apiv1.Composition{}
		if err := cli.Get(ctx, key, comp); err != nil {
			return false, nil
		}
		if comp.Status.CurrentSynthesis == nil {
			return false, nil
		}
		gen := comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration
		if gen <= minGen {
			t.Logf("composition %s: waiting for synth gen > %d (current: %d)", key.Name, minGen, gen)
			return false, nil
		}
		if comp.Status.Simplified == nil || comp.Status.Simplified.Status != "Ready" {
			t.Logf("composition %s: synth gen advanced to %d, waiting for Ready (current: %s)", key.Name, gen, comp.Status.Simplified.String())
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err, "timed out waiting for composition %s to re-synthesize past gen %d", key.Name, minGen)
}

// waitForResourceExists polls until the given object can be fetched.
func waitForResourceExists(t *testing.T, ctx context.Context, cli client.Client, obj client.Object, timeout time.Duration) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		if err := cli.Get(ctx, key, obj); err != nil {
			return false, nil
		}
		return true, nil
	})
	require.NoError(t, err, "timed out waiting for %s %s to exist", obj.GetObjectKind().GroupVersionKind().Kind, key)
}

// waitForResourceGone polls until the given object returns NotFound.
func waitForResourceGone(t *testing.T, ctx context.Context, cli client.Client, obj client.Object, timeout time.Duration) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		err := cli.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err, "timed out waiting for %s %s to be deleted", obj.GetObjectKind().GroupVersionKind().Kind, key)
}

// waitForSymphonyReady polls until the symphony's Status.Ready is non-nil.
func waitForSymphonyReady(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, timeout time.Duration) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		sym := &apiv1.Symphony{}
		if err := cli.Get(ctx, key, sym); err != nil {
			return false, nil
		}
		return sym.Status.Ready != nil, nil
	})
	require.NoError(t, err, "timed out waiting for symphony %s to become Ready", key.Name)
}

// newMinimalSynthesizer builds a Synthesizer that outputs a ConfigMap with the given data.
// It uses ubuntu:latest with a bash command to echo a KRM ResourceList.
func newMinimalSynthesizer(name, cmName, key, value string) *apiv1.Synthesizer {
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

// newComposition builds a Composition referencing a synthesizer by name.
func newComposition(name, ns, synthName string) *apiv1.Composition {
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

// cleanup deletes an object and waits for it to be gone.
func cleanup(t *testing.T, ctx context.Context, cli client.Client, obj client.Object) {
	t.Helper()
	err := cli.Delete(ctx, obj)
	if apierrors.IsNotFound(err) {
		return
	}
	require.NoError(t, err, "failed to delete %s", obj.GetName())
	waitForResourceGone(t, ctx, cli, obj, 60*time.Second)
}

// configMap returns a ConfigMap object reference for use with wait helpers.
func configMap(name, ns string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
}
