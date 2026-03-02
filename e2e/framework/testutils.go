package framework

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// CompositionPredicate is a function that inspects a Composition and returns true when the
// desired condition is met. The message return value is used for logging while polling.
type CompositionPredicate func(*apiv1.Composition) (done bool, msg string)

// WaitForCompositionAsExpected polls until the given predicate returns true for the composition.
func WaitForCompositionAsExpected(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, timeout time.Duration, pred CompositionPredicate) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		comp := &apiv1.Composition{}
		if err := cli.Get(ctx, key, comp); err != nil {
			return false, nil
		}
		done, msg := pred(comp)
		if !done {
			t.Logf("composition %s: %s", key.Name, msg)
		}
		return done, nil
	})
	require.NoError(t, err, "timed out waiting for composition %s to meet expected condition", key.Name)
}

// CompositionIsReady returns a predicate that checks whether the composition's
// Simplified.Status equals "Ready".
func CompositionIsReady() CompositionPredicate {
	return func(comp *apiv1.Composition) (bool, string) {
		if comp.Status.Simplified == nil {
			return false, "no simplified status yet"
		}
		return comp.Status.Simplified.Status == "Ready",
			fmt.Sprintf("status: %s", comp.Status.Simplified.String())
	}
}

// CompositionResynthesized returns a predicate that checks whether the composition's
// ObservedSynthesizerGeneration has advanced beyond minGen AND status is "Ready".
func CompositionResynthesized(minGen int64) CompositionPredicate {
	return func(comp *apiv1.Composition) (bool, string) {
		if comp.Status.CurrentSynthesis == nil {
			return false, "no current synthesis yet"
		}
		gen := comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration
		if gen <= minGen {
			return false, fmt.Sprintf("waiting for synth gen > %d (current: %d)", minGen, gen)
		}
		if comp.Status.Simplified == nil || comp.Status.Simplified.Status != "Ready" {
			return false, fmt.Sprintf("synth gen advanced to %d, waiting for Ready (current: %s)", gen, comp.Status.Simplified.String())
		}
		return true, ""
	}
}

// WaitForCompositionReady polls until the composition's Simplified.Status equals "Ready".
func WaitForCompositionReady(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, timeout time.Duration) {
	t.Helper()
	WaitForCompositionAsExpected(t, ctx, cli, key, timeout, CompositionIsReady())
}

// WaitForCompositionResynthesized polls until the composition's ObservedSynthesizerGeneration
// advances beyond minGen AND status returns to "Ready".
func WaitForCompositionResynthesized(t *testing.T, ctx context.Context, cli client.Client, key types.NamespacedName, minGen int64, timeout time.Duration) {
	t.Helper()
	WaitForCompositionAsExpected(t, ctx, cli, key, timeout, CompositionResynthesized(minGen))
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
