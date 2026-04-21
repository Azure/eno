package e2e

import (
    "context"
    "testing"
    "time"

    apiv1 "github.com/Azure/eno/api/v1"
    "github.com/Azure/eno/internal/testutil"
    krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

/*
This test verifies that:
- An override using CEL condition + valueProgram
- Is evaluated during reconciliation
- And mutates the synthesized resource correctly
*/
func TestOverrides_CELValueProgram_EndToEnd(t *testing.T) {
    ctx := testutil.NewContext(t)

    // testutil.NewManager wires all controllers internally
    mgr := testutil.NewManager(t)
    upstream := mgr.GetClient()

    // Fake synthesizer executor:
    // Emits a ConfigMap containing a CEL override.
    testutil.WithFakeExecutor(t, mgr, func(
        ctx context.Context,
        s *apiv1.Synthesizer,
        input *krmv1.ResourceList,
    ) (*krmv1.ResourceList, error) {

        return &krmv1.ResourceList{
            Items: []*unstructured.Unstructured{
                {
                    Object: map[string]any{
                        "apiVersion": "v1",
                        "kind":       "ConfigMap",
                        "metadata": map[string]any{
                            "name":      "override-test-cm",
                            "namespace": "default",
                            "annotations": map[string]any{
                                // Compact JSON avoids annotation parsing issues
                                "eno.azure.io/overrides":
                                    `[{"path":"self.data.foo","condition":"has(self.data.foo)","valueProgram":"self.data.foo + '-overridden'"}]`,
                            },
                        },
                        "data": map[string]any{
                            "foo": "original",
                        },
                    },
                },
            },
        }, nil
    })

    // Start envtest manager (handles cache sync internally)
    go mgr.Start(t)

    // --- Create Synthesizer (cluster-scoped) ---
    synth := &apiv1.Synthesizer{
        ObjectMeta: metav1.ObjectMeta{
            Name: "override-test-synth",
        },
        Spec: apiv1.SynthesizerSpec{
            Image: "fake-image",
            Refs: []apiv1.Ref{
                {
                    Key: "config",
                    Resource: apiv1.ResourceRef{
                        Group:   "",
                        Version: "v1",
                        Kind:    "ConfigMap",
                    },
                },
            },
        },
    }
    require.NoError(t, upstream.Create(ctx, synth))

    // --- Create Composition ---
    comp := &apiv1.Composition{
        ObjectMeta: metav1.ObjectMeta{
            Name:      "override-test-comp",
            Namespace: "default",
        },
        Spec: apiv1.CompositionSpec{
            Synthesizer: apiv1.SynthesizerRef{
                Name: synth.Name,
            },
        },
    }
    require.NoError(t, upstream.Create(ctx, comp))

    // --- Assertion ---
    // Eventually the override should apply and mutate the value.
    cm := &corev1.ConfigMap{}
    require.Eventually(t, func() bool {
        err := mgr.DownstreamClient.Get(
            ctx,
            client.ObjectKey{
                Name:      "override-test-cm",
                Namespace: "default",
            },
            cm,
        )
        return err == nil && cm.Data["foo"] == "original-overridden"
    }, 30*time.Second, 500*time.Millisecond)

    assert.Equal(t, "original-overridden", cm.Data["foo"])
}