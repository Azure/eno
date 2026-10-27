package e2e

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	fw "github.com/Azure/eno/e2e/framework"
)

// TestSynthesizerInputRemoval verifies that removing a ref from a synthesizer doesn't block resynthesis.
//
// The composition keeps its binding for the removed ref (as when bindings are inherited from a Symphony),
// and the removed input carries a synthesizer-generation annotation. Unless its input revision is pruned,
// that stale revision holds the composition out of lockstep and the new synthesis never happens.
func TestSynthesizerInputRemoval(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	synthName := fw.UniqueName("input-removal-synth")
	compName := fw.UniqueName("input-removal-comp")
	outputName := fw.UniqueName("input-removal-output")
	compKey := types.NamespacedName{Name: compName, Namespace: "default"}

	configA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: fw.UniqueName("input-removal-configa"), Namespace: "default"},
		Data:       map[string]string{"valueA": "alpha"},
	}
	configB := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fw.UniqueName("input-removal-configb"),
			Namespace: "default",
			// Ties configb's revision to the synthesizer's first generation, so it goes stale once the ref is removed
			Annotations: map[string]string{"eno.azure.io/synthesizer-generation": "1"},
		},
		Data: map[string]string{"valueB": "bravo"},
	}

	// Copies every "valueA"/"valueB" entry found in the inputs into the output ConfigMap
	script := fmt.Sprintf(`data=$(grep -o '"value[AB]":"[^"]*"' | sort -u | paste -sd, - || true)
printf '{"apiVersion":"config.kubernetes.io/v1","kind":"ResourceList","items":[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s","namespace":"default"},"data":{%%s}}]}' "$data"`, outputName)

	configMapRef := apiv1.ResourceRef{Version: "v1", Kind: "ConfigMap"}
	synth := fw.NewMinimalSynthesizer(synthName, fw.WithCommand([]string{"/bin/bash", "-c", script}))
	synth.Spec.Refs = []apiv1.Ref{
		{Key: "configa", Resource: configMapRef},
		{Key: "configb", Resource: configMapRef},
	}

	comp := fw.NewComposition(compName, "default", fw.WithSynthesizerRefs(apiv1.SynthesizerRef{Name: synthName}))
	comp.Spec.Bindings = []apiv1.Binding{
		{Key: "configa", Resource: apiv1.ResourceBinding{Name: configA.Name, Namespace: configA.Namespace}},
		{Key: "configb", Resource: apiv1.ResourceBinding{Name: configB.Name, Namespace: configB.Namespace}},
	}

	var initialSynthGen int64

	// -- Define workflow steps --

	createConfigA := fw.CreateStep(t, "createConfigA", cli, configA)
	createConfigB := fw.CreateStep(t, "createConfigB", cli, configB)
	createSynthesizer := fw.CreateStep(t, "createSynthesizer", cli, synth)
	createComposition := fw.CreateStep(t, "createComposition", cli, comp)

	waitReady := flow.Func("waitReady", func(ctx context.Context) error {
		fw.WaitForCompositionReady(t, ctx, cli, compKey, 3*time.Minute)
		current := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKey, current))
		require.NotNil(t, current.Status.CurrentSynthesis)
		initialSynthGen = current.Status.CurrentSynthesis.ObservedSynthesizerGeneration
		t.Logf("initial ObservedSynthesizerGeneration: %d", initialSynthGen)
		return nil
	})

	verifyInitialOutput := flow.Func("verifyInitialOutput", func(ctx context.Context) error {
		waitForOutputData(t, ctx, cli, outputName, map[string]string{"valueA": "alpha", "valueB": "bravo"})
		t.Log("verified output contains both inputs")
		return nil
	})

	removeConfigBRef := flow.Func("removeConfigBRef", func(ctx context.Context) error {
		require.NoError(t, cli.Get(ctx, types.NamespacedName{Name: synthName}, synth))
		synth.Spec.Refs = []apiv1.Ref{{Key: "configa", Resource: configMapRef}}
		t.Log("removing configb ref from synthesizer (composition binding is left in place)")
		return cli.Update(ctx, synth)
	})

	waitResynthesized := flow.Func("waitResynthesized", func(ctx context.Context) error {
		fw.WaitForCompositionResynthesized(t, ctx, cli, compKey, initialSynthGen, 3*time.Minute)
		t.Log("composition re-synthesized and ready")
		return nil
	})

	verifyUpdatedOutput := flow.Func("verifyUpdatedOutput", func(ctx context.Context) error {
		waitForOutputData(t, ctx, cli, outputName, map[string]string{"valueA": "alpha"})

		current := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKey, current))
		for _, rev := range current.Status.InputRevisions {
			require.NotEqual(t, "configb", rev.Key, "input revision for removed ref should be pruned")
		}
		t.Log("verified output contains only the remaining input")
		return nil
	})

	deleteComposition := fw.DeleteStep(t, "deleteComposition", cli, comp)

	verifyOutputDeleted := flow.Func("verifyOutputDeleted", func(ctx context.Context) error {
		output := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: outputName, Namespace: "default"}}
		fw.WaitForResourceDeleted(t, ctx, cli, output, 60*time.Second)
		return nil
	})

	cleanup := fw.CleanupStep(t, "cleanup", cli, synth, configA, configB)

	// -- Wire the DAG --

	w := new(flow.Workflow)
	w.Add(
		flow.Step(createComposition).DependsOn(createConfigA, createConfigB, createSynthesizer),
		flow.Step(waitReady).DependsOn(createComposition),
		flow.Step(verifyInitialOutput).DependsOn(waitReady),
		flow.Step(removeConfigBRef).DependsOn(verifyInitialOutput),
		flow.Step(waitResynthesized).DependsOn(removeConfigBRef),
		flow.Step(verifyUpdatedOutput).DependsOn(waitResynthesized),
		flow.Step(deleteComposition).DependsOn(verifyUpdatedOutput),
		flow.Step(verifyOutputDeleted).DependsOn(deleteComposition),
		flow.Step(cleanup).DependsOn(verifyOutputDeleted),
	)

	require.NoError(t, w.Do(ctx))
}

// waitForOutputData polls until the output ConfigMap's data exactly matches expected.
func waitForOutputData(t *testing.T, ctx context.Context, cli client.Client, name string, expected map[string]string) {
	t.Helper()
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, time.Minute, true, func(ctx context.Context) (bool, error) {
		output := &corev1.ConfigMap{}
		if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, output); err != nil {
			return false, nil
		}
		t.Logf("output configmap %s data: %v", name, output.Data)
		return reflect.DeepEqual(expected, output.Data), nil
	})
	require.NoError(t, err, "timed out waiting for output configmap %s to have data %v", name, expected)
}
