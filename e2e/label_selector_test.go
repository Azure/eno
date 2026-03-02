package e2e

import (
	"context"
	"fmt"
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

func TestLabelSelectorSynthesizerResolution(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	synthNameV1 := fw.UniqueName("selector-e2e-v1")
	synthNameV2 := fw.UniqueName("selector-e2e-v2")
	compName := fw.UniqueName("selector-e2e-comp")
	cmName := fw.UniqueName("selector-e2e-cm")

	// ConfigMaps produced by each synthesizer version.
	cmV1 := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "default"},
		Data:       map[string]string{"version": "v1"},
	}
	cmV2 := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "default"},
		Data:       map[string]string{"version": "v2"},
	}

	// Use a unique label value so this test's selector won't match synthesizers from other tests.
	appLabel := compName // reuse the unique comp name as the app label value

	synthV1 := fw.NewMinimalSynthesizer(synthNameV1,
		fw.WithLabels(map[string]string{"app": appLabel, "version": "v1"}),
		fw.WithCommand(fw.ToCommand(cmV1)),
	)
	synthV2 := fw.NewMinimalSynthesizer(synthNameV2,
		fw.WithLabels(map[string]string{"app": appLabel, "version": "v2"}),
		fw.WithCommand(fw.ToCommand(cmV2)),
	)

	comp := fw.NewComposition(compName, "default", fw.WithSynthesizerRefs(apiv1.SynthesizerRef{
		LabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{"app": appLabel, "version": "v1"},
		},
	}))
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	// -- Define workflow steps --

	createSynthV1 := fw.CreateStep(t, "createSynthV1", cli, synthV1)
	createSynthV2 := fw.CreateStep(t, "createSynthV2", cli, synthV2)

	createComposition := fw.CreateStep(t, "createComposition", cli, comp)

	waitV1Ready := flow.Func("waitV1Ready", func(ctx context.Context) error {
		fw.WaitForCompositionReady(t, ctx, cli, compKey, 3*time.Minute)
		return nil
	})

	verifyV1Output := flow.Func("verifyV1Output", func(ctx context.Context) error {
		// Verify resolved synth name.
		require.NoError(t, cli.Get(ctx, compKey, comp))
		require.NotNil(t, comp.Status.Simplified, "simplified status should be set")
		assert.Equal(t, synthNameV1, comp.Status.Simplified.ResolvedSynthName,
			"should resolve to v1 synthesizer")

		// Verify the ConfigMap has v1 data.
		cm := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "default"},
		}
		fw.WaitForResourceExists(t, ctx, cli, &cm, 30*time.Second)
		assert.Equal(t, "v1", cm.Data["version"], "expected v1 ConfigMap")
		t.Log("verified v1 synthesis output")
		return nil
	})

	updateSelector := flow.Func("updateSelector", func(ctx context.Context) error {
		// Re-fetch to get latest resourceVersion.
		require.NoError(t, cli.Get(ctx, compKey, comp))
		comp.Spec.Synthesizer.LabelSelector.MatchLabels = map[string]string{
			"app":     appLabel,
			"version": "v2",
		}
		t.Log("updating composition label selector to target v2")
		return cli.Update(ctx, comp)
	})

	waitV2Ready := flow.Func("waitV2Ready", func(ctx context.Context) error {
		fw.WaitForCompositionAsExpected(t, ctx, cli, compKey, 3*time.Minute,
			func(c *apiv1.Composition) (bool, string) {
				if c.Status.Simplified == nil {
					return false, "waiting for simplified status"
				}
				if c.Status.Simplified.ResolvedSynthName != synthNameV2 {
					return false, fmt.Sprintf("resolvedSynthName=%q, want %q",
						c.Status.Simplified.ResolvedSynthName, synthNameV2)
				}
				if c.Status.Simplified.Status != "Ready" {
					return false, fmt.Sprintf("status=%q, want Ready", c.Status.Simplified.Status)
				}
				return true, ""
			})
		t.Log("composition resolved to v2 synthesizer and is Ready")
		return nil
	})

	verifyV2Output := flow.Func("verifyV2Output", func(ctx context.Context) error {
		// Verify the ConfigMap now has v2 data.
		cm := corev1.ConfigMap{}
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: cmName, Namespace: "default"}, &cm))
		assert.Equal(t, "v2", cm.Data["version"], "expected v2 ConfigMap")
		t.Log("verified v2 ConfigMap output")
		return nil
	})

	deleteComposition := fw.DeleteStep(t, "deleteComposition", cli, comp)

	verifyOutputDeleted := flow.Func("verifyOutputDeleted", func(ctx context.Context) error {
		cm := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "default"},
		}
		fw.WaitForResourceDeleted(t, ctx, cli, &cm, 60*time.Second)
		t.Log("verified ConfigMap deleted")
		return nil
	})

	cleanup := fw.CleanupStep(t, "cleanup", cli, synthV1, synthV2)

	// -- Wire the DAG --

	w := new(flow.Workflow)
	w.Add(
		// Both synthesizers can be created in parallel; composition depends on both.
		flow.Step(createComposition).DependsOn(createSynthV1, createSynthV2),
		flow.Step(waitV1Ready).DependsOn(createComposition),
		flow.Step(verifyV1Output).DependsOn(waitV1Ready),

		// Switch selector to v2.
		flow.Step(updateSelector).DependsOn(verifyV1Output),
		flow.Step(waitV2Ready).DependsOn(updateSelector),
		flow.Step(verifyV2Output).DependsOn(waitV2Ready),

		// Cleanup.
		flow.Step(deleteComposition).DependsOn(verifyV2Output),
		flow.Step(verifyOutputDeleted).DependsOn(deleteComposition),
		flow.Step(cleanup).DependsOn(verifyOutputDeleted),
	)

	require.NoError(t, w.Do(ctx))
}
