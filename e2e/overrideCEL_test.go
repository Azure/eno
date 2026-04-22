package e2e

import (
    "context"
    "testing"
    "time"

    flow "github.com/Azure/go-workflow"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/types"

    apiv1 "github.com/Azure/eno/api/v1"
    fw "github.com/Azure/eno/e2e/framework"
)

func TestOverrides_CELValueProgram_EndToEnd(t *testing.T) {
    t.Parallel()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    cli := fw.NewClient(t)

    synthName := fw.UniqueName("override-cel-synth")
    compName := fw.UniqueName("override-cel-comp")
    cmName := fw.UniqueName("override-cel-cm")

    // ✅ Idempotent override — always produces the same value
    overrideJSON := `[{
        "path": "self.data.foo",
        "condition": "has(self.data.foo)",
        "valueProgram": "'cel-override-value'"
    }]`

    cm := &corev1.ConfigMap{
        TypeMeta: metav1.TypeMeta{
            APIVersion: "v1",
            Kind:       "ConfigMap",
        },
        ObjectMeta: metav1.ObjectMeta{
            Name:      cmName,
            Namespace: "default",
            Annotations: map[string]string{
                "eno.azure.io/overrides": overrideJSON,
            },
        },
        Data: map[string]string{
            "foo": "original",
        },
    }

    synth := fw.NewMinimalSynthesizer(
        synthName,
        fw.WithCommand(fw.ToCommand(cm)),
    )

    comp := fw.NewComposition(
        compName,
        "default",
        fw.WithSynthesizerRefs(apiv1.SynthesizerRef{Name: synthName}),
    )

    compKey := types.NamespacedName{
        Name:      compName,
        Namespace: "default",
    }

    createSynth := fw.CreateStep(t, "createSynthesizer", cli, synth)
    createComp := fw.CreateStep(t, "createComposition", cli, comp)

    waitReady := flow.Func("waitReady", func(ctx context.Context) error {
        fw.WaitForCompositionReady(t, ctx, cli, compKey, 3*time.Minute)
        return nil
    })

    verifyOverrideApplied := flow.Func("verifyOverrideApplied", func(ctx context.Context) error {
        got := &corev1.ConfigMap{
            ObjectMeta: metav1.ObjectMeta{
                Name:      cmName,
                Namespace: "default",
            },
        }
        fw.WaitForResourceExists(t, ctx, cli, got, 60*time.Second)
        assert.Equal(t, "cel-override-value", got.Data["foo"])
        return nil
    })

    deleteComp := fw.DeleteStep(t, "deleteComposition", cli, comp)

    verifyCMDeleted := flow.Func("verifyConfigMapDeleted", func(ctx context.Context) error {
        obj := &corev1.ConfigMap{
            ObjectMeta: metav1.ObjectMeta{
                Name:      cmName,
                Namespace: "default",
            },
        }
        fw.WaitForResourceDeleted(t, ctx, cli, obj, 2*time.Minute)
        return nil
    })

    cleanupSynth := fw.CleanupStep(t, "cleanupSynthesizer", cli, synth)

    w := new(flow.Workflow)
    w.Add(
        flow.Step(createComp).DependsOn(createSynth),
        flow.Step(waitReady).DependsOn(createComp),
        flow.Step(verifyOverrideApplied).DependsOn(waitReady),
        flow.Step(deleteComp).DependsOn(verifyOverrideApplied),
        flow.Step(verifyCMDeleted).DependsOn(deleteComp),
        flow.Step(cleanupSynth).DependsOn(verifyCMDeleted),
    )

    require.NoError(t, w.Do(ctx))
}
