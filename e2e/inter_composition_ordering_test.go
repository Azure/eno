package e2e

import (
	"context"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	fw "github.com/Azure/eno/e2e/framework"
	flow "github.com/Azure/go-workflow"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// testHoldFinalizer is added to C's ConfigMap by the test to delay
	// composition C's cleanup, giving time to verify A and B are blocked.
	testHoldFinalizer = "e2e-test/hold"
)

func TestInterCompositionOrderingTest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	// Initialize the Synthesizer names
	synthesizerNameA := fw.UniqueName("dep-synth-a")
	synthesizerNameB := fw.UniqueName("dep-synth-b")
	synthesizerNameC := fw.UniqueName("dep-synth-c")
	symphonyName := fw.UniqueName("dep-symphony")

	cmNameA := fw.UniqueName("dep-cm-a")
	cmNameB := fw.UniqueName("dep-cm-b")
	cmNameC := fw.UniqueName("dep-cm-c")

	// --- ConfigMaps produced by each synthesizer
	cmA := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmNameA, Namespace: "default"},
		Data:       map[string]string{"source": "A"},
	}
	cmB := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmNameB, Namespace: "default"},
		Data:       map[string]string{"source": "B"},
	}
	cmC := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmNameC,
			Namespace: "default",
			Annotations: map[string]string{
				"eno.azure.io/deletion-strategy": "foreground",
			},
		},
		Data: map[string]string{"source": "C"},
	}

	// Add synthesizer
	synthA := fw.NewMinimalSynthesizer(synthesizerNameA, fw.WithCommand(fw.ToCommand(cmA)))
	synthB := fw.NewMinimalSynthesizer(synthesizerNameB, fw.WithCommand(fw.ToCommand(cmB)))
	synthC := fw.NewMinimalSynthesizer(synthesizerNameC, fw.WithCommand(fw.ToCommand(cmC)))

	// Generate the symphony
	symphony := &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{Name: symphonyName, Namespace: "default"},
		Spec: apiv1.SymphonySpec{
			Variations: []apiv1.Variation{
				{
					Synthesizer: apiv1.SynthesizerRef{Name: synthesizerNameA},
					// No dependencies
				},
				{
					Synthesizer: apiv1.SynthesizerRef{Name: synthesizerNameB},
					DependsOn: []apiv1.VariationDependency{
						{Synthesizer: synthesizerNameA},
					},
				},
				{
					Synthesizer: apiv1.SynthesizerRef{Name: synthesizerNameC},
					DependsOn: []apiv1.VariationDependency{
						{Synthesizer: synthesizerNameB},
					},
				},
			},
		},
	}

	symphonyKey := types.NamespacedName{Name: symphonyName, Namespace: "default"}

	// Adding the test steps
	createSynthA := fw.CreateStep(t, "createSynthA", cli, synthA)
	createSynthB := fw.CreateStep(t, "createSynthB", cli, synthB)
	createSynthC := fw.CreateStep(t, "createSynthC", cli, synthC)
	createSymphony := fw.CreateStep(t, "createSymphony", cli, symphony)

	waitSymphonyReady := flow.Func("waitSymphonyReady", func(ctx context.Context) error {
		fw.WaitForSymphonyReady(t, ctx, cli, symphonyKey, 5*time.Minute)
		t.Log("symphony is ready")
		return nil
	})

	verifyCreationOrder := flow.Func("verifyCreationOrder", func(ctx context.Context) error {
		compList := &apiv1.CompositionList{}
		require.NoError(t, cli.List(ctx, compList, client.InNamespace("default")))

		// Find compositions owned by this symphony, keyed by synthesizer name
		compBySynth := map[string]*apiv1.Composition{}
		for i := range compList.Items {
			c := &compList.Items[i]
			for _, ref := range c.OwnerReferences {
				if ref.Name == symphonyName {
					compBySynth[c.Spec.Synthesizer.Name] = c
				}
			}
		}

		require.Contains(t, compBySynth, synthesizerNameA, "composition for synth-a should exist")
		require.Contains(t, compBySynth, synthesizerNameB, "composition for synth-b should exist")
		require.Contains(t, compBySynth, synthesizerNameC, "composition for synth-c should exist")

		synthA := compBySynth[synthesizerNameA].Status.CurrentSynthesis
		synthB := compBySynth[synthesizerNameB].Status.CurrentSynthesis
		synthC := compBySynth[synthesizerNameC].Status.CurrentSynthesis

		require.NotNil(t, synthA, "A should have current synthesis")
		require.NotNil(t, synthB, "B should have current synthesis")
		require.NotNil(t, synthC, "C should have current synthesis")
		require.NotNil(t, synthA.Ready, "A should be ready")
		require.NotNil(t, synthB.Ready, "B should be ready")
		require.NotNil(t, synthC.Ready, "C should be ready")

		readyA := synthA.Ready
		readyB := synthB.Ready
		readyC := synthC.Ready

		t.Logf("Ready timestamps: A=%s, B=%s, C=%s", readyA.Time, readyB.Time, readyC.Time)

		// A must be ready before or at the same time as B
		require.False(t, readyA.Time.After(readyB.Time),
			"A (ready=%s) should be ready before B (ready=%s)", readyA.Time, readyB.Time)
		// B must be ready before or at the same time as C
		require.False(t, readyB.Time.After(readyC.Time),
			"B (ready=%s) should be ready before C (ready=%s)", readyB.Time, readyC.Time)

		t.Log("creation order verified: A → B → C")
		return nil
	})

	// Capture composition keys BEFORE deleting the symphony, otherwise
	// Kubernetes GC via ownerReferences may delete them before we can list them.
	var compKeyA, compKeyB, compKeyC types.NamespacedName

	captureCompositionKeys := flow.Func("captureCompositionKeys", func(ctx context.Context) error {
		compList := &apiv1.CompositionList{}
		require.NoError(t, cli.List(ctx, compList, client.InNamespace("default")))

		for i := range compList.Items {
			c := &compList.Items[i]
			for _, ref := range c.OwnerReferences {
				if ref.Name == symphonyName {
					key := types.NamespacedName{Name: c.Name, Namespace: c.Namespace}
					// Log dependsOn for diagnostics
					t.Logf("composition %s (synth=%s) dependsOn=%v", c.Name, c.Spec.Synthesizer.Name, c.Spec.DependsOn)
					switch c.Spec.Synthesizer.Name {
					case synthesizerNameA:
						compKeyA = key
					case synthesizerNameB:
						compKeyB = key
					case synthesizerNameC:
						compKeyC = key
					}
				}
			}
		}

		require.NotEmpty(t, compKeyA.Name, "composition A key should be captured")
		require.NotEmpty(t, compKeyB.Name, "composition B key should be captured")
		require.NotEmpty(t, compKeyC.Name, "composition C key should be captured")
		t.Logf("captured composition keys: A=%s, B=%s, C=%s", compKeyA, compKeyB, compKeyC)
		return nil
	})

	// Add a test finalizer to C's ConfigMap BEFORE deleting the symphony.
	// When the symphony is deleted, the reconciliation controller tries to delete
	// C's managed resources. The finalizer on C's ConfigMap prevents it from being
	// fully removed, which stalls C's composition cleanup. While C is stuck:
	//   - B cannot delete because C (its dependent) still exists
	//   - A cannot delete because B (its dependent) still exists
	// After 3 seconds we verify A and B are still blocked, then remove the finalizer
	// to let the deletion cascade proceed.
	addConfigMapFinalizer := flow.Func("addConfigMapFinalizer", func(ctx context.Context) error {
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: cmNameC, Namespace: "default"}
		require.NoError(t, cli.Get(ctx, cmKey, cm))
		if controllerutil.AddFinalizer(cm, testHoldFinalizer) {
			require.NoError(t, cli.Update(ctx, cm))
			t.Logf("added test finalizer to ConfigMap %s", cmNameC)
		}
		return nil
	})

	deleteSymphony := fw.DeleteStep(t, "deleteSymphony", cli, symphony)

	// Verify deletion order by tracking the order in which the controller removes
	// the eno cleanup finalizer from each composition. The test finalizers keep
	// the objects alive so we can observe this reliably.
	//
	// INVARIANTS:
	//   - A's eno finalizer must NOT be removed while B still has it (B depends on A)
	//   - B's eno finalizer must NOT be removed while C still has it (C depends on B)
	verifyDeletionOrder := flow.Func("verifyDeletionOrder", func(ctx context.Context) error {
		// Wait 3 seconds — C's ConfigMap finalizer stalls C's cleanup,
		// so the controller should be blocking A and B's deletion.
		t.Log("waiting 3 seconds for controller to process deletions...")
		time.Sleep(3 * time.Second)

		// Hard assertion: A, B, and C must still exist
		compA := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKeyA, compA),
			"composition A should still exist (B depends on A, and B is blocked by C)")
		t.Log("after 3s: A still exists (blocked)")

		compB := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKeyB, compB),
			"composition B should still exist (C depends on B, and C is stuck)")
		t.Log("after 3s: B still exists (blocked)")

		compC := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKeyC, compC),
			"composition C should still exist (its ConfigMap finalizer is holding it)")
		t.Log("after 3s: C still exists (ConfigMap finalizer holding)")

		// Now remove the test finalizer from C's ConfigMap to unblock the cascade
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: cmNameC, Namespace: "default"}
		require.NoError(t, cli.Get(ctx, cmKey, cm))
		if controllerutil.RemoveFinalizer(cm, testHoldFinalizer) {
			require.NoError(t, cli.Update(ctx, cm))
			t.Logf("removed test finalizer from ConfigMap %s — deletion cascade should proceed", cmNameC)
		}

		// Now poll for all three compositions to be fully deleted, tracking order
		existsA, existsB, existsC := true, true, true
		var deletionOrder []string

		err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
			nowA, nowB, nowC := true, true, true

			if existsA {
				comp := &apiv1.Composition{}
				if err := cli.Get(ctx, compKeyA, comp); apierrors.IsNotFound(err) {
					nowA = false
				}
			} else {
				nowA = false
			}

			if existsB {
				comp := &apiv1.Composition{}
				if err := cli.Get(ctx, compKeyB, comp); apierrors.IsNotFound(err) {
					nowB = false
				}
			} else {
				nowB = false
			}

			if existsC {
				comp := &apiv1.Composition{}
				if err := cli.Get(ctx, compKeyC, comp); apierrors.IsNotFound(err) {
					nowC = false
				}
			} else {
				nowC = false
			}

			// CRITICAL INVARIANTS:
			require.False(t, !nowA && nowB,
				"ORDERING VIOLATION: A was deleted while B still exists")
			require.False(t, !nowB && nowC,
				"ORDERING VIOLATION: B was deleted while C still exists")

			if existsC && !nowC {
				t.Log("C deleted")
				deletionOrder = append(deletionOrder, "C")
			}
			if existsB && !nowB {
				t.Log("B deleted")
				deletionOrder = append(deletionOrder, "B")
			}
			if existsA && !nowA {
				t.Log("A deleted")
				deletionOrder = append(deletionOrder, "A")
			}

			existsA, existsB, existsC = nowA, nowB, nowC
			return !existsA && !existsB && !existsC, nil
		})
		require.NoError(t, err, "timed out waiting for all compositions to be deleted")

		t.Logf("deletion order: %v", deletionOrder)
		t.Log("deletion ordering verified: C → B → A")
		return nil
	})

	// Remove test finalizers so Kubernetes GC can fully delete the compositions
	removeTestFinalizers := flow.Func("removeTestFinalizers", func(ctx context.Context) error {
		// Clean up the test finalizer from C's ConfigMap if it still exists
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: cmNameC, Namespace: "default"}
		if err := cli.Get(ctx, cmKey, cm); err == nil {
			if controllerutil.RemoveFinalizer(cm, testHoldFinalizer) {
				require.NoError(t, cli.Update(ctx, cm))
				t.Logf("removed leftover test finalizer from ConfigMap %s", cmNameC)
			}
		}
		return nil
	})

	cleanupAll := fw.CleanupStep(t, "cleanupAll", cli, cmA, cmB, cmC, synthA, synthB, synthC)

	// Register cleanup to run even if the test fails
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		// Remove test finalizer from C's ConfigMap if it still exists
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: cmNameC, Namespace: "default"}
		if err := cli.Get(cleanupCtx, cmKey, cm); err == nil {
			if controllerutil.RemoveFinalizer(cm, testHoldFinalizer) {
				_ = cli.Update(cleanupCtx, cm)
			}
		}
		for _, obj := range []client.Object{cmA, cmB, cmC, synthA, synthB, synthC} {
			fw.Cleanup(t, cleanupCtx, cli, obj)
		}
	})

	// Create DAG
	w := new(flow.Workflow)
	w.Add(
		// Create the synthesizer in parallel, then create symphony
		flow.Step(createSymphony).DependsOn(createSynthA, createSynthB, createSynthC),
		flow.Step(waitSymphonyReady).DependsOn(createSymphony),
		flow.Step(verifyCreationOrder).DependsOn(waitSymphonyReady),

		// Capture composition keys, add test finalizers, then delete symphony
		flow.Step(captureCompositionKeys).DependsOn(verifyCreationOrder),
		flow.Step(addConfigMapFinalizer).DependsOn(captureCompositionKeys),
		flow.Step(deleteSymphony).DependsOn(addConfigMapFinalizer),
		flow.Step(verifyDeletionOrder).DependsOn(deleteSymphony),
		flow.Step(removeTestFinalizers).DependsOn(verifyDeletionOrder),

		// Cleanup in DAG path (best-effort, t.Cleanup above is the safety net)
		flow.Step(cleanupAll).DependsOn(removeTestFinalizers),
	)

	require.NoError(t, w.Do(ctx))
}
