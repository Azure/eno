package framework

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// WaitForCompositionReady polls until the composition's Simplified.Status equals "Ready".
func WaitForCompositionReady(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, timeout time.Duration) {
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

// WaitForCompositionResynthesized polls until the composition's ObservedSynthesizerGeneration
// advances beyond minGen AND status returns to "Ready".
func WaitForCompositionResynthesized(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, minGen int64, timeout time.Duration) {
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

// WaitForSymphonyReady polls until the symphony's Status.Ready is non-nil.
func WaitForSymphonyReady(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, timeout time.Duration) {
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

// WaitForResourceExists polls until the given object can be fetched.
func WaitForResourceExists(t *testing.T, ctx context.Context, cli client.Client, obj client.Object, timeout time.Duration) {
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

// WaitForResourceDeleted polls until the given object returns NotFound.
func WaitForResourceDeleted(t *testing.T, ctx context.Context, cli client.Client, obj client.Object, timeout time.Duration) {
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
