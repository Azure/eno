package e2e

import (
	"context"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	apiv1 "github.com/Azure/eno/api/v1"
)

func TestSymphonyLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := newClient(t)

	synthName1 := uniqueName("sym-synth-1")
	synthName2 := uniqueName("sym-synth-2")
	symphonyName := uniqueName("sym-test")
	cmName1 := uniqueName("sym-cm-1")
	cmName2 := uniqueName("sym-cm-2")

	synth1 := newMinimalSynthesizer(synthName1, cmName1, "source", "synth1")
	synth2 := newMinimalSynthesizer(synthName2, cmName2, "source", "synth2")

	symphony := &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{
			Name:      symphonyName,
			Namespace: "default",
		},
		Spec: apiv1.SymphonySpec{
			Variations: []apiv1.Variation{
				{Synthesizer: apiv1.SynthesizerRef{Name: synthName1}},
				{Synthesizer: apiv1.SynthesizerRef{Name: synthName2}},
			},
		},
	}

	symphonyKey := types.NamespacedName{Name: symphonyName, Namespace: "default"}

	// -- Steps --

	createSynth1 := flow.Func("createSynth1", func(ctx context.Context) error {
		t.Log("creating synthesizer", synthName1)
		return cli.Create(ctx, synth1)
	})

	createSynth2 := flow.Func("createSynth2", func(ctx context.Context) error {
		t.Log("creating synthesizer", synthName2)
		return cli.Create(ctx, synth2)
	})

	createSymphony := flow.Func("createSymphony", func(ctx context.Context) error {
		t.Log("creating symphony", symphonyName)
		return cli.Create(ctx, symphony)
	})

	waitSymphonyReady := flow.Func("waitSymphonyReady", func(ctx context.Context) error {
		waitForSymphonyReady(t, ctx, cli, symphonyKey, 3*time.Minute)
		t.Log("symphony is ready")
		return nil
	})

	verifyBothConfigMaps := flow.Func("verifyBothConfigMaps", func(ctx context.Context) error {
		cm1 := configMap(cmName1, "default")
		cm2 := configMap(cmName2, "default")
		waitForResourceExists(t, ctx, cli, cm1, 30*time.Second)
		waitForResourceExists(t, ctx, cli, cm2, 30*time.Second)
		t.Log("both ConfigMaps exist")
		return nil
	})

	deleteSymphony := flow.Func("deleteSymphony", func(ctx context.Context) error {
		t.Log("deleting symphony", symphonyName)
		return cli.Delete(ctx, symphony)
	})

	verifyCleanup := flow.Func("verifyCleanup", func(ctx context.Context) error {
		cm1 := configMap(cmName1, "default")
		cm2 := configMap(cmName2, "default")
		waitForResourceGone(t, ctx, cli, cm1, 60*time.Second)
		waitForResourceGone(t, ctx, cli, cm2, 60*time.Second)
		t.Log("both ConfigMaps deleted")
		return nil
	})

	cleanupSynthesizers := flow.Func("cleanupSynthesizers", func(ctx context.Context) error {
		cleanup(t, ctx, cli, synth1)
		cleanup(t, ctx, cli, synth2)
		t.Log("synthesizers cleaned up")
		return nil
	})

	// -- Wire the DAG --
	// createSynth1 and createSynth2 run in parallel (no mutual dependency).

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createSymphony).DependsOn(createSynth1, createSynth2),
		flow.Step(waitSymphonyReady).DependsOn(createSymphony),
		flow.Step(verifyBothConfigMaps).DependsOn(waitSymphonyReady),
		flow.Step(deleteSymphony).DependsOn(verifyBothConfigMaps),
		flow.Step(verifyCleanup).DependsOn(deleteSymphony),
		flow.Step(cleanupSynthesizers).DependsOn(verifyCleanup),
	)

	require.NoError(t, w.Do(ctx))
}
