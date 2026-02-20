package e2e

import (
	"context"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	fw "github.com/Azure/eno/test/e2e/framework"
)

func TestSymphonyLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	synthName1 := fw.UniqueName("sym-synth-1")
	synthName2 := fw.UniqueName("sym-synth-2")
	symphonyName := fw.UniqueName("sym-test")
	cmName1 := fw.UniqueName("sym-cm-1")
	cmName2 := fw.UniqueName("sym-cm-2")

	cm1 := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmName1, Namespace: "default"},
		Data:       map[string]string{"source": "synth1"},
	}
	cm2 := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmName2, Namespace: "default"},
		Data:       map[string]string{"source": "synth2"},
	}

	synth1 := fw.NewMinimalSynthesizer(synthName1, fw.WithCommand(fw.ToCommand(cm1)))
	synth2 := fw.NewMinimalSynthesizer(synthName2, fw.WithCommand(fw.ToCommand(cm2)))

	symphony := fw.NewSymphony(symphonyName, "default", synthName1, synthName2)
	symphonyKey := types.NamespacedName{Name: symphonyName, Namespace: "default"}

	// -- Steps --

	createSynth1 := fw.CreateStep(t, "createSynth1", cli, synth1)

	createSynth2 := fw.CreateStep(t, "createSynth2", cli, synth2)

	createSymphony := fw.CreateStep(t, "createSymphony", cli, symphony)

	waitSymphonyReady := flow.Func("waitSymphonyReady", func(ctx context.Context) error {
		fw.WaitForSymphonyReady(t, ctx, cli, symphonyKey, 3*time.Minute)
		t.Log("symphony is ready")
		return nil
	})

	verifySymphonyExists := flow.Func("verifySymphonyExists", func(ctx context.Context) error {
		sym := &apiv1.Symphony{}
		err := cli.Get(ctx, symphonyKey, sym)
		require.NoError(t, err, "symphony should exist")
		t.Log("symphony exists")
		return nil
	})

	verifyCompositionsExist := flow.Func("verifyCompositionsExist", func(ctx context.Context) error {
		compList := &apiv1.CompositionList{}
		err := cli.List(ctx, compList, client.InNamespace("default"))
		require.NoError(t, err)
		count := 0
		for _, c := range compList.Items {
			for _, ref := range c.OwnerReferences {
				if ref.Name == symphonyName {
					count++
				}
			}
		}
		require.Equal(t, 2, count, "expected 2 compositions owned by symphony")
		t.Log("2 compositions exist")
		return nil
	})

	verifyResourceSlicesExist := flow.Func("verifyResourceSlicesExist", func(ctx context.Context) error {
		sliceList := &apiv1.ResourceSliceList{}
		err := cli.List(ctx, sliceList, client.InNamespace("default"))
		require.NoError(t, err)
		require.NotEmpty(t, sliceList.Items, "expected at least one ResourceSlice")
		t.Logf("%d ResourceSlice(s) exist", len(sliceList.Items))
		return nil
	})

	verifySynthesizersExist := flow.Func("verifySynthesizersExist", func(ctx context.Context) error {
		s1 := &apiv1.Synthesizer{}
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: synthName1}, s1), "synthesizer 1 should exist")
		s2 := &apiv1.Synthesizer{}
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: synthName2}, s2), "synthesizer 2 should exist")
		t.Log("both synthesizers exist")
		return nil
	})

	verifyConfigMapsExist := flow.Func("verifyConfigMapsExist", func(ctx context.Context) error {
		cm1 := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName1, Namespace: "default"},
		}
		cm2 := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName2, Namespace: "default"},
		}
		fw.WaitForResourceExists(t, ctx, cli, &cm1, 30*time.Second)
		fw.WaitForResourceExists(t, ctx, cli, &cm2, 30*time.Second)
		t.Log("both ConfigMaps exist")
		return nil
	})

	deleteSymphony := fw.DeleteStep(t, "deleteSymphony", cli, symphony)

	verifyCleanup := flow.Func("verifyCleanup", func(ctx context.Context) error {
		// Symphony deletion orphans managed resources (by design), so the
		// ConfigMaps should still exist after the symphony and its
		// compositions are removed.
		fw.WaitForResourceDeleted(t, ctx, cli, symphony, 60*time.Second)
		t.Log("symphony is gone")

		cm1 := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName1, Namespace: "default"},
		}
		cm2 := corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName2, Namespace: "default"},
		}
		fw.WaitForResourceExists(t, ctx, cli, &cm1, 30*time.Second)
		fw.WaitForResourceExists(t, ctx, cli, &cm2, 30*time.Second)
		t.Log("both ConfigMaps still exist (orphaned)")
		return nil
	})

	cleanupSynthesizers := fw.CleanupStep(t, "cleanupAll", cli, cm1, cm2, synth1, synth2)

	// -- Wire the DAG --
	// createSynth1 and createSynth2 run in parallel (no mutual dependency).

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createSymphony).DependsOn(createSynth1, createSynth2),
		flow.Step(waitSymphonyReady).DependsOn(createSymphony),

		// Parallel verification — all depend on waitSymphonyReady
		flow.Step(verifySymphonyExists).DependsOn(waitSymphonyReady),
		flow.Step(verifyCompositionsExist).DependsOn(waitSymphonyReady),
		flow.Step(verifyResourceSlicesExist).DependsOn(waitSymphonyReady),
		flow.Step(verifySynthesizersExist).DependsOn(waitSymphonyReady),
		flow.Step(verifyConfigMapsExist).DependsOn(waitSymphonyReady),

		// deleteSymphony waits for all verifications
		flow.Step(deleteSymphony).DependsOn(verifySymphonyExists, verifyCompositionsExist, verifyResourceSlicesExist, verifySynthesizersExist, verifyConfigMapsExist),
		flow.Step(verifyCleanup).DependsOn(deleteSymphony),
		flow.Step(cleanupSynthesizers).DependsOn(verifyCleanup),
	)

	require.NoError(t, w.Do(ctx))
}
