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

// TestInterCompositionOrderingLifecycleTest exercises the full lifecycle of compositions
// with inter-composition ordering: initial creation, adding new variations with dependencies,
// removing variations (simulating feature disable with delayed resource cleanup), and finally
// namespace deletion to verify the symphony-deleting flow with DependsOn compositions.
//
// Dependency graph (after phase 2):
//
//	synth-a (root, no deps)
//	├── synth-b (depends on synth-a)
//	│   └── synthc-ol (depends on synth-b AND synthc-ul)
//	└── synthc-ul (depends on synth-a)
//	    └── synthc-ol (depends on synth-b AND synthc-ul)
func TestInterCompositionOrderingLifecycleTest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cli := fw.NewClient(t)

	// Resource namespace
	testNS := fw.UniqueName("testing-ns1")

	// Synthesizer names
	synthNameA := fw.UniqueName("lc-synth-a")
	synthNameB := fw.UniqueName("lc-synth-b")
	synthNameCOL := fw.UniqueName("lc-synthc-ol")
	synthNameCUL := fw.UniqueName("lc-synthc-ul")
	symphonyName := fw.UniqueName("lc-symphony")

	// ConfigMap names
	cmNameA := fw.UniqueName("lc-cm-a")
	cmNameB := fw.UniqueName("lc-cm-b")
	cmNameCOL := fw.UniqueName("lc-cm-col")
	cmNameCUL := fw.UniqueName("lc-cm-cul")

	// ConfigMaps produced by each synthesizer (all in testNS)
	cmA := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmNameA, Namespace: testNS},
		Data:       map[string]string{"source": "A"},
	}
	cmB := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmNameB, Namespace: testNS},
		Data:       map[string]string{"source": "B"},
	}
	cmCOL := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmNameCOL,
			Namespace: testNS,
			Annotations: map[string]string{
				"eno.azure.io/deletion-strategy": "foreground",
			},
		},
		Data: map[string]string{"source": "COL"},
	}
	cmCUL := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: cmNameCUL, Namespace: testNS},
		Data:       map[string]string{"source": "CUL"},
	}

	// Synthesizers
	synthA := fw.NewMinimalSynthesizer(synthNameA, fw.WithCommand(fw.ToCommand(cmA)))
	synthB := fw.NewMinimalSynthesizer(synthNameB, fw.WithCommand(fw.ToCommand(cmB)))
	synthCOL := fw.NewMinimalSynthesizer(synthNameCOL, fw.WithCommand(fw.ToCommand(cmCOL)))
	synthCUL := fw.NewMinimalSynthesizer(synthNameCUL, fw.WithCommand(fw.ToCommand(cmCUL)))

	// Namespace object
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: testNS},
	}

	// Phase 1 symphony: only synth-a and synth-b
	symphony := &apiv1.Symphony{
		ObjectMeta: metav1.ObjectMeta{Name: symphonyName, Namespace: testNS},
		Spec: apiv1.SymphonySpec{
			Variations: []apiv1.Variation{
				{
					Synthesizer: apiv1.SynthesizerRef{Name: synthNameA},
				},
				{
					Synthesizer: apiv1.SynthesizerRef{Name: synthNameB},
					DependsOn: []apiv1.VariationDependency{
						{Synthesizer: synthNameA},
					},
				},
			},
		},
	}

	symphonyKey := types.NamespacedName{Name: symphonyName, Namespace: testNS}

	// --- Safety net cleanup ---
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()

		// Remove any test finalizers from ConfigMaps
		for _, cmName := range []string{cmNameCOL} {
			cm := &corev1.ConfigMap{}
			cmKey := types.NamespacedName{Name: cmName, Namespace: testNS}
			if err := cli.Get(cleanupCtx, cmKey, cm); err == nil {
				if controllerutil.RemoveFinalizer(cm, testHoldFinalizer) {
					_ = cli.Update(cleanupCtx, cm)
				}
			}
		}
		for _, obj := range []client.Object{synthA, synthB, synthCOL, synthCUL} {
			fw.Cleanup(t, cleanupCtx, cli, obj)
		}
		// Delete namespace last (cascades everything inside)
		_ = cli.Delete(cleanupCtx, ns)
	})

	// ========== WORKFLOW STEPS ==========

	createNS := fw.CreateStep(t, "createNamespace", cli, ns)
	createSynthA := fw.CreateStep(t, "createSynthA", cli, synthA)
	createSynthB := fw.CreateStep(t, "createSynthB", cli, synthB)
	createSynthCOL := fw.CreateStep(t, "createSynthCOL", cli, synthCOL)
	createSynthCUL := fw.CreateStep(t, "createSynthCUL", cli, synthCUL)
	createSymphony := fw.CreateStep(t, "createSymphony", cli, symphony)

	// --- Phase 1: Wait for symphony ready with synth-a and synth-b ---
	waitPhase1Ready := flow.Func("waitPhase1Ready", func(ctx context.Context) error {
		fw.WaitForSymphonyReady(t, ctx, cli, symphonyKey, 5*time.Minute)
		t.Log("phase 1: symphony is ready (synth-a, synth-b)")
		return nil
	})

	// --- Phase 1: Verify creation ordering A → B ---
	verifyPhase1Order := flow.Func("verifyPhase1Order", func(ctx context.Context) error {
		compBySynth := getCompsBySynth(t, ctx, cli, testNS, symphonyName)

		require.Contains(t, compBySynth, synthNameA)
		require.Contains(t, compBySynth, synthNameB)

		synA := compBySynth[synthNameA].Status.CurrentSynthesis
		synB := compBySynth[synthNameB].Status.CurrentSynthesis
		require.NotNil(t, synA)
		require.NotNil(t, synB)
		require.NotNil(t, synA.Ready, "A should be ready")
		require.NotNil(t, synB.Ready, "B should be ready")

		t.Logf("phase 1 ready timestamps: A=%s, B=%s", synA.Ready.Time, synB.Ready.Time)
		require.False(t, synA.Ready.Time.After(synB.Ready.Time),
			"A (ready=%s) should be ready before B (ready=%s)", synA.Ready.Time, synB.Ready.Time)

		t.Log("phase 1 creation order verified: A → B")
		return nil
	})

	// --- Phase 2: Add synthc-ol and synthc-ul to the symphony ---
	updateSymphonyPhase2 := flow.Func("updateSymphonyPhase2", func(ctx context.Context) error {
		sym := &apiv1.Symphony{}
		require.NoError(t, cli.Get(ctx, symphonyKey, sym))

		sym.Spec.Variations = []apiv1.Variation{
			{
				Synthesizer: apiv1.SynthesizerRef{Name: synthNameA},
			},
			{
				Synthesizer: apiv1.SynthesizerRef{Name: synthNameB},
				DependsOn: []apiv1.VariationDependency{
					{Synthesizer: synthNameA},
				},
			},
			{
				Synthesizer: apiv1.SynthesizerRef{Name: synthNameCUL},
				DependsOn: []apiv1.VariationDependency{
					{Synthesizer: synthNameA},
				},
			},
			{
				Synthesizer: apiv1.SynthesizerRef{Name: synthNameCOL},
				DependsOn: []apiv1.VariationDependency{
					{Synthesizer: synthNameB},
					{Synthesizer: synthNameCUL},
				},
			},
		}

		require.NoError(t, cli.Update(ctx, sym))
		t.Log("phase 2: updated symphony to add synthc-ol and synthc-ul")
		return nil
	})

	waitPhase2Ready := flow.Func("waitPhase2Ready", func(ctx context.Context) error {
		// Wait for all 4 compositions to be ready
		err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			compBySynth := getCompsBySynth(t, ctx, cli, testNS, symphonyName)
			for _, name := range []string{synthNameA, synthNameB, synthNameCOL, synthNameCUL} {
				comp, ok := compBySynth[name]
				if !ok {
					return false, nil
				}
				if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Ready == nil {
					return false, nil
				}
			}
			return true, nil
		})
		require.NoError(t, err, "timed out waiting for phase 2 symphony to become ready")
		t.Log("phase 2: all 4 compositions are ready")
		return nil
	})

	// --- Phase 2: Verify ordering ---
	// synthc-ul depends on synth-a, synthc-ol depends on synth-b AND synthc-ul
	// So: A ready before CUL, B ready before COL, CUL ready before COL
	verifyPhase2Order := flow.Func("verifyPhase2Order", func(ctx context.Context) error {
		compBySynth := getCompsBySynth(t, ctx, cli, testNS, symphonyName)

		synA := compBySynth[synthNameA].Status.CurrentSynthesis
		synB := compBySynth[synthNameB].Status.CurrentSynthesis
		synCOL := compBySynth[synthNameCOL].Status.CurrentSynthesis
		synCUL := compBySynth[synthNameCUL].Status.CurrentSynthesis

		t.Logf("phase 2 ready timestamps: A=%s, B=%s, CUL=%s, COL=%s",
			synA.Ready.Time, synB.Ready.Time, synCUL.Ready.Time, synCOL.Ready.Time)

		require.False(t, synA.Ready.Time.After(synCUL.Ready.Time),
			"A should be ready before CUL")
		require.False(t, synB.Ready.Time.After(synCOL.Ready.Time),
			"B should be ready before COL")
		require.False(t, synCUL.Ready.Time.After(synCOL.Ready.Time),
			"CUL should be ready before COL")

		t.Log("phase 2 ordering verified: A → {B, CUL} → COL")
		return nil
	})

	// --- Phase 3: Remove synthc-ol and synthc-ul (simulate feature disable) ---
	// First add a test finalizer to synthc-ol's ConfigMap to simulate slow CRD cleanup
	addCOLFinalizer := flow.Func("addCOLFinalizer", func(ctx context.Context) error {
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: cmNameCOL, Namespace: testNS}
		require.NoError(t, cli.Get(ctx, cmKey, cm))
		if controllerutil.AddFinalizer(cm, testHoldFinalizer) {
			require.NoError(t, cli.Update(ctx, cm))
			t.Logf("phase 3: added test finalizer to ConfigMap %s", cmNameCOL)
		}
		return nil
	})

	// Capture composition keys for COL and CUL before removing them
	var compKeyCOL, compKeyCUL types.NamespacedName

	capturePhase3Keys := flow.Func("capturePhase3Keys", func(ctx context.Context) error {
		compBySynth := getCompsBySynth(t, ctx, cli, testNS, symphonyName)
		compKeyCOL = client.ObjectKeyFromObject(compBySynth[synthNameCOL])
		compKeyCUL = client.ObjectKeyFromObject(compBySynth[synthNameCUL])
		t.Logf("phase 3: captured keys COL=%s, CUL=%s", compKeyCOL, compKeyCUL)
		return nil
	})

	// Update symphony to remove synthc-ol and synthc-ul
	updateSymphonyPhase3 := flow.Func("updateSymphonyPhase3", func(ctx context.Context) error {
		sym := &apiv1.Symphony{}
		require.NoError(t, cli.Get(ctx, symphonyKey, sym))

		sym.Spec.Variations = []apiv1.Variation{
			{
				Synthesizer: apiv1.SynthesizerRef{Name: synthNameA},
			},
			{
				Synthesizer: apiv1.SynthesizerRef{Name: synthNameB},
				DependsOn: []apiv1.VariationDependency{
					{Synthesizer: synthNameA},
				},
			},
		}

		require.NoError(t, cli.Update(ctx, sym))
		t.Log("phase 3: removed synthc-ol and synthc-ul from symphony")
		return nil
	})

	// Verify deletion ordering for removed variations:
	// synthc-ol (leaf, depends on synthc-ul) should delete first
	// synthc-ul should wait until synthc-ol is gone
	// The finalizer on COL's ConfigMap stalls its cleanup
	verifyPhase3DeletionOrder := flow.Func("verifyPhase3DeletionOrder", func(ctx context.Context) error {
		// Wait 3 seconds — COL's ConfigMap finalizer stalls cleanup
		t.Log("phase 3: waiting 3s for controller to process deletions...")
		time.Sleep(3 * time.Second)

		// Both COL and CUL should still exist (COL is stuck, CUL is blocked by COL)
		compCOL := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKeyCOL, compCOL),
			"COL should still exist (its ConfigMap finalizer is holding it)")
		t.Log("phase 3: after 3s COL still exists (ConfigMap finalizer holding)")

		compCUL := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, compKeyCUL, compCUL),
			"CUL should still exist (COL depends on CUL, COL is stuck)")
		t.Log("phase 3: after 3s CUL still exists (blocked by COL)")

		// Now remove the test finalizer to unblock the cascade
		cm := &corev1.ConfigMap{}
		cmKey := types.NamespacedName{Name: cmNameCOL, Namespace: testNS}
		require.NoError(t, cli.Get(ctx, cmKey, cm))
		if controllerutil.RemoveFinalizer(cm, testHoldFinalizer) {
			require.NoError(t, cli.Update(ctx, cm))
			t.Logf("phase 3: removed test finalizer from ConfigMap %s", cmNameCOL)
		}

		// Poll until both COL and CUL are deleted, verifying ordering invariant
		existsCOL, existsCUL := true, true
		var deletionOrder []string

		err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
			nowCOL, nowCUL := true, true

			if existsCOL {
				comp := &apiv1.Composition{}
				if err := cli.Get(ctx, compKeyCOL, comp); apierrors.IsNotFound(err) {
					nowCOL = false
				}
			} else {
				nowCOL = false
			}

			if existsCUL {
				comp := &apiv1.Composition{}
				if err := cli.Get(ctx, compKeyCUL, comp); apierrors.IsNotFound(err) {
					nowCUL = false
				}
			} else {
				nowCUL = false
			}

			// INVARIANT: CUL must not be deleted while COL still exists
			require.False(t, !nowCUL && nowCOL,
				"ORDERING VIOLATION: CUL was deleted while COL still exists")

			if existsCOL && !nowCOL {
				t.Log("phase 3: COL deleted")
				deletionOrder = append(deletionOrder, "COL")
			}
			if existsCUL && !nowCUL {
				t.Log("phase 3: CUL deleted")
				deletionOrder = append(deletionOrder, "CUL")
			}

			existsCOL, existsCUL = nowCOL, nowCUL
			return !existsCOL && !existsCUL, nil
		})
		require.NoError(t, err, "timed out waiting for COL and CUL to be deleted")
		t.Logf("phase 3 deletion order: %v", deletionOrder)
		t.Log("phase 3 ordering verified: COL → CUL")
		return nil
	})

	// --- Phase 4: Delete the namespace to test symphony-deleting flow with DependsOn ---
	deleteNamespace := flow.Func("deleteNamespace", func(ctx context.Context) error {
		t.Logf("phase 4: deleting namespace %s", testNS)
		return cli.Delete(ctx, ns)
	})

	verifyPhase4Cleanup := flow.Func("verifyPhase4Cleanup", func(ctx context.Context) error {
		// Wait for all compositions to be gone (namespace deletion cascades)
		err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
			compList := &apiv1.CompositionList{}
			if err := cli.List(ctx, compList, client.InNamespace(testNS)); err != nil {
				// Namespace may be gone already
				return true, nil
			}
			remaining := 0
			for i := range compList.Items {
				for _, ref := range compList.Items[i].OwnerReferences {
					if ref.Name == symphonyName {
						remaining++
						t.Logf("phase 4: composition %s (synth=%s) still exists",
							compList.Items[i].Name, compList.Items[i].Spec.Synthesizer.Name)
					}
				}
			}
			if remaining > 0 {
				t.Logf("phase 4: %d compositions remaining", remaining)
				return false, nil
			}
			return true, nil
		})
		require.NoError(t, err, "timed out waiting for compositions to be cleaned up after namespace deletion")
		t.Log("phase 4: all compositions cleaned up after namespace deletion")
		return nil
	})

	cleanupSynthesizers := fw.CleanupStep(t, "cleanupSynthesizers", cli, synthA, synthB, synthCOL, synthCUL)

	// ========== WORKFLOW DAG ==========
	w := new(flow.Workflow)
	w.Add(
		// Setup: create namespace and all synthesizers in parallel, then symphony
		flow.Step(createSymphony).DependsOn(createNS, createSynthA, createSynthB, createSynthCOL, createSynthCUL),

		// Phase 1: verify initial creation ordering (A → B)
		flow.Step(waitPhase1Ready).DependsOn(createSymphony),
		flow.Step(verifyPhase1Order).DependsOn(waitPhase1Ready),

		// Phase 2: add synthc-ol and synthc-ul, verify ordering
		flow.Step(updateSymphonyPhase2).DependsOn(verifyPhase1Order),
		flow.Step(waitPhase2Ready).DependsOn(updateSymphonyPhase2),
		flow.Step(verifyPhase2Order).DependsOn(waitPhase2Ready),

		// Phase 3: remove synthc-ol and synthc-ul with delayed cleanup
		flow.Step(capturePhase3Keys).DependsOn(verifyPhase2Order),
		flow.Step(addCOLFinalizer).DependsOn(capturePhase3Keys),
		flow.Step(updateSymphonyPhase3).DependsOn(addCOLFinalizer),
		flow.Step(verifyPhase3DeletionOrder).DependsOn(updateSymphonyPhase3),

		// Phase 4: delete namespace, verify cleanup without stuck compositions
		flow.Step(deleteNamespace).DependsOn(verifyPhase3DeletionOrder),
		flow.Step(verifyPhase4Cleanup).DependsOn(deleteNamespace),

		// Final cleanup of cluster-scoped synthesizers
		flow.Step(cleanupSynthesizers).DependsOn(verifyPhase4Cleanup),
	)

	require.NoError(t, w.Do(ctx))
}

// getCompsBySynth lists compositions in the given namespace owned by the named symphony
// and returns them keyed by synthesizer name.
func getCompsBySynth(t *testing.T, ctx context.Context, cli client.Client, ns, symphonyName string) map[string]*apiv1.Composition {
	t.Helper()
	compList := &apiv1.CompositionList{}
	require.NoError(t, cli.List(ctx, compList, client.InNamespace(ns)))

	result := map[string]*apiv1.Composition{}
	for i := range compList.Items {
		c := &compList.Items[i]
		for _, ref := range c.OwnerReferences {
			if ref.Name == symphonyName {
				result[c.Spec.Synthesizer.Name] = c
			}
		}
	}
	return result
}
