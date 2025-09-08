package reconciliation

import (
	"context"
	"strings"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestErrorReporting(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()

	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		output.Items = []*unstructured.Unstructured{{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      "test-obj",
					"namespace": "default",
				},
				"data": "wrongType",
			},
		}}
		if s.Spec.Image == "fixed" {
			output.Items[0].Object["data"] = map[string]any{"foo": "bar"}
		}
		return output, nil
	})

	registerControllers(t, mgr)
	setupTestSubject(t, mgr)
	mgr.Start(t)
	syn, comp := writeGenericComposition(t, upstream)

	// An error should eventually be reported in the composition
	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.Simplified != nil && comp.Status.Simplified.Error != ""
	})
	t.Logf("simplified error: %s", comp.Status.Simplified.Error)

	// Update the synth to return a valid resource - the composition should eventually become ready
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		upstream.Get(ctx, client.ObjectKeyFromObject(syn), syn)
		syn.Spec.Image = "fixed"
		return upstream.Update(ctx, syn)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := upstream.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil &&
			comp.Status.Simplified != nil && comp.Status.Simplified.Error == "" &&
			comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil
	})
}

func TestSummarizeErrorIntegration(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	upstream := mgr.GetClient()
	mgr.Start(t)

	test := func(t *testing.T, fn func() error) {
		a := summarizeError(fn())
		b := summarizeError(fn())
		assert.Equal(t, a, b, "the error message should be stable across operations")
		t.Logf("message for %q: %s", t.Name(), a)
	}

	t.Run("invalid-field/create", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
				"data": "wrongType",
			},
		}
		test(t, func() error {
			return upstream.Create(ctx, obj.DeepCopy())
		})
	})

	t.Run("invalid-field/update", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		require.NoError(t, upstream.Create(ctx, obj))

		obj.Object["data"] = "invalid"
		test(t, func() error {
			return upstream.Create(ctx, obj.DeepCopy())
		})
	})

	t.Run("invalid-field/apply", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		require.NoError(t, upstream.Create(ctx, obj))

		obj.Object["data"] = "invalid"
		test(t, func() error {
			return upstream.Patch(ctx, obj.DeepCopy(), client.Apply, client.FieldOwner("eno"))
		})
	})

	t.Run("conflict/update", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		require.NoError(t, upstream.Create(ctx, obj))

		obj.SetResourceVersion("10000000")
		assert.Empty(t, summarizeError(upstream.Update(ctx, obj)))
	})

	t.Run("conflict/apply", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		require.NoError(t, upstream.Create(ctx, obj))

		obj.SetResourceVersion("10000000")
		assert.Empty(t, summarizeError(upstream.Patch(ctx, obj, client.Apply, client.FieldOwner("eno"))))
	})

	t.Run("conflict/create", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		require.NoError(t, upstream.Create(ctx, obj.DeepCopy()))
		assert.Empty(t, summarizeError(upstream.Create(ctx, obj)))
	})

	t.Run("unknown-kind/create", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Unknown",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		test(t, func() error {
			return upstream.Create(ctx, obj.DeepCopy())
		})
	})

	t.Run("unknown-kind/delete", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Unknown",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		test(t, func() error {
			return upstream.Delete(ctx, obj.DeepCopy())
		})
	})

	t.Run("unknown-group/create", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "foo/v1",
				"kind":       "Unknown",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		test(t, func() error {
			return upstream.Create(ctx, obj.DeepCopy())
		})
	})

	t.Run("unknown-kind/delete", func(t *testing.T) {
		obj := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "foo/v1",
				"kind":       "Unknown",
				"metadata": map[string]any{
					"name":      strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
					"namespace": "default",
				},
			},
		}
		test(t, func() error {
			return upstream.Delete(ctx, obj.DeepCopy())
		})
	})
}
