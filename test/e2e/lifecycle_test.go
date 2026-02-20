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
)

func TestMinimalLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := newClient(t)

	synthName := uniqueName("lifecycle-synth")
	compName := uniqueName("lifecycle-comp")
	cmName := uniqueName("lifecycle-cm")

	synth := newMinimalSynthesizer(synthName, cmName, "someKey", "initialValue")
	comp := newComposition(compName, "default", synthName)
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	// Track the initial synthesizer generation after creation.
	var initialSynthGen int64

	// -- Define workflow steps --

	createSynthesizer := flow.Func("createSynthesizer", func(ctx context.Context) error {
		t.Log("creating synthesizer", synthName)
		return cli.Create(ctx, synth)
	})

	createComposition := flow.Func("createComposition", func(ctx context.Context) error {
		t.Log("creating composition", compName)
		return cli.Create(ctx, comp)
	})

	waitReady := flow.Func("waitReady", func(ctx context.Context) error {
		waitForCompositionReady(t, ctx, cli, compKey, 3*time.Minute)
		// Capture the initial synthesizer generation.
		require.NoError(t, cli.Get(ctx, compKey, comp))
		require.NotNil(t, comp.Status.CurrentSynthesis)
		initialSynthGen = comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration
		t.Logf("initial ObservedSynthesizerGeneration: %d", initialSynthGen)
		return nil
	})

	verifyOutputConfigMap := flow.Func("verifyOutputConfigMap", func(ctx context.Context) error {
		cm := configMap(cmName, "default")
		waitForResourceExists(t, ctx, cli, cm, 30*time.Second)
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
		waitForCompositionResynthesized(t, ctx, cli, compKey, initialSynthGen, 3*time.Minute)
		t.Log("composition re-synthesized and ready")
		return nil
	})

	verifyUpdatedOutput := flow.Func("verifyUpdatedOutput", func(ctx context.Context) error {
		cm := configMap(cmName, "default")
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: cmName, Namespace: "default"}, cm))
		assert.Equal(t, "updatedValue", cm.Data["someKey"], "expected updated ConfigMap value")
		t.Log("verified updated ConfigMap output")
		return nil
	})

	deleteComposition := flow.Func("deleteComposition", func(ctx context.Context) error {
		t.Log("deleting composition", compName)
		return cli.Delete(ctx, comp)
	})

	verifyOutputDeleted := flow.Func("verifyOutputDeleted", func(ctx context.Context) error {
		cm := configMap(cmName, "default")
		waitForResourceGone(t, ctx, cli, cm, 60*time.Second)
		t.Log("verified ConfigMap deleted")
		return nil
	})

	cleanupSynthesizer := flow.Func("cleanupSynthesizer", func(ctx context.Context) error {
		cleanup(t, ctx, cli, synth)
		t.Log("synthesizer cleaned up")
		return nil
	})

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
