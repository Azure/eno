package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	fw "github.com/Azure/eno/test/e2e/framework"
)

func TestMinimalLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	synthName := fw.UniqueName("lifecycle-synth")
	compName := fw.UniqueName("lifecycle-comp")
	cmName := fw.UniqueName("lifecycle-cm")

	synth := fw.NewMinimalSynthesizer(synthName, cmName, "someKey", "initialValue")
	comp := fw.NewComposition(compName, "default", synthName)
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	// Track the initial synthesizer generation after creation.
	var initialSynthGen int64

	// -- Define workflow steps --

	createSynthesizer := fw.CreateStep(t, "createSynthesizer", cli, synth)
	createComposition := fw.CreateStep(t, "createComposition", cli, comp)

	waitReady := flow.Func("waitReady", func(ctx context.Context) error {
		fw.WaitForCompositionReady(t, ctx, cli, compKey, 3*time.Minute)
		// Capture the initial synthesizer generation.
		require.NoError(t, cli.Get(ctx, compKey, comp))
		require.NotNil(t, comp.Status.CurrentSynthesis)
		initialSynthGen = comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration
		t.Logf("initial ObservedSynthesizerGeneration: %d", initialSynthGen)
		return nil
	})

	verifyOutputConfigMap := flow.Func("verifyOutputConfigMap", func(ctx context.Context) error {
		cm := fw.ConfigMap(cmName, "default")
		fw.WaitForResourceExists(t, ctx, cli, cm, 30*time.Second)
		assert.Equal(t, "initialValue", cm.Data["someKey"], "expected initial ConfigMap value")
		t.Log("verified initial ConfigMap output")
		return nil
	})

	updateSynthesizer := flow.Func("updateSynthesizer", func(ctx context.Context) error {
		// Re-fetch to get latest resourceVersion.
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: synthName}, synth))
		synth.Spec.Command = []string{
			"/bin/bash", "-c",
			fmt.Sprintf(`echo '{"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s","namespace":"default"},"data":{"someKey":"updatedValue"}}]}'`, cmName),
		}
		t.Log("updating synthesizer to produce updatedValue")
		return cli.Update(ctx, synth)
	})

	waitReadyAfterResynthesis := flow.Func("waitReadyAfterResynthesis", func(ctx context.Context) error {
		fw.WaitForCompositionResynthesized(t, ctx, cli, compKey, initialSynthGen, 3*time.Minute)
		t.Log("composition re-synthesized and ready")
		return nil
	})

	verifyUpdatedOutput := flow.Func("verifyUpdatedOutput", func(ctx context.Context) error {
		cm := fw.ConfigMap(cmName, "default")
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: cmName, Namespace: "default"}, cm))
		assert.Equal(t, "updatedValue", cm.Data["someKey"], "expected updated ConfigMap value")
		t.Log("verified updated ConfigMap output")
		return nil
	})

	deleteComposition := fw.DeleteStep(t, "deleteComposition", cli, comp)

	verifyOutputDeleted := flow.Func("verifyOutputDeleted", func(ctx context.Context) error {
		cm := fw.ConfigMap(cmName, "default")
		fw.WaitForResourceGone(t, ctx, cli, cm, 60*time.Second)
		t.Log("verified ConfigMap deleted")
		return nil
	})

	cleanupSynthesizer := fw.CleanupStep(t, "cleanupSynthesizer", cli, synth)

	// -- Wire the DAG --

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createComposition).DependsOn(createSynthesizer),
		flow.Step(waitReady).DependsOn(createComposition),
		flow.Step(verifyOutputConfigMap).DependsOn(waitReady),
		flow.Step(updateSynthesizer).DependsOn(verifyOutputConfigMap),
		flow.Step(waitReadyAfterResynthesis).DependsOn(updateSynthesizer),
		flow.Step(verifyUpdatedOutput).DependsOn(waitReadyAfterResynthesis),
		flow.Step(deleteComposition).DependsOn(verifyUpdatedOutput),
		flow.Step(verifyOutputDeleted).DependsOn(deleteComposition),
		flow.Step(cleanupSynthesizer).DependsOn(verifyOutputDeleted),
	)

	require.NoError(t, w.Do(ctx))
}
