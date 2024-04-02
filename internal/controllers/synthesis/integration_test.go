package synthesis

import (
	"fmt"
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

var minimalTestConfig = &Config{
	SliceCreationQPS: 15,
	PodNamespace:     "default",
}

// TestControllerHappyPath proves that pods are eventually created and synthesizers are eventually executed
// to synthesize composition creates and updates.
func TestControllerHappyPath(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager))
	conn := &testutil.ExecConn{}
	require.NoError(t, NewExecController(mgr.Manager, minimalTestConfig, conn))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	t.Run("initial creation", func(t *testing.T) {
		// The pod eventually performs the synthesis
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
		})
	})

	t.Run("composition update", func(t *testing.T) {
		// Updating the composition should cause re-synthesis
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			comp.Spec.Bindings = []apiv1.Binding{{Key: "new-binding", Resource: apiv1.ResourceBinding{Name: "test"}}}
			return cli.Update(ctx, comp)
		})
		require.NoError(t, err)

		latest := comp.Generation
		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration >= latest
		})

		// The previous state is retained
		if comp.Status.PreviousSynthesis == nil {
			t.Error("state wasn't swapped to previous")
		}
	})

	// The synthesizer is eventually executed a second time
	testutil.Eventually(t, func() bool {
		return conn.Calls.Load() == 2
	})

	// The pod is deleted
	testutil.Eventually(t, func() bool {
		list := &corev1.PodList{}
		require.NoError(t, cli.List(ctx, list))
		return len(list.Items) == 0
	})
}

// TestPodNamespaceOverride proves that synthesis Pods are scheduled on the
// configured namespace.
func TestPodNamespaceOverride(t *testing.T) {
	expectedPodNamespace := "eno"
	var actualPodNamespace atomic.Value
	podHook := func(p *corev1.Pod) {
		actualPodNamespace.Store(p.Namespace)
	}

	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t, testutil.WithPodNamespace(expectedPodNamespace))
	cli := mgr.GetClient()

	lifecycleConfig := *minimalTestConfig
	lifecycleConfig.PodNamespace = expectedPodNamespace
	require.NoError(t, NewPodLifecycleController(mgr.Manager, &lifecycleConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager))
	conn := &testutil.ExecConn{
		PodHook: podHook,
	}
	require.NoError(t, NewExecController(mgr.Manager, &lifecycleConfig, conn))
	mgr.Start(t)

	ns := &corev1.Namespace{}
	ns.Name = "eno"
	require.NoError(t, cli.Create(ctx, ns))

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	t.Run("initial creation", func(t *testing.T) {
		// The pod eventually performs the synthesis
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
		})
	})

	require.Equal(t, expectedPodNamespace, actualPodNamespace.Load())
	require.Equal(t, 1, conn.Calls.Load())

	// The pod is deleted
	testutil.Eventually(t, func() bool {
		list := &corev1.PodList{}
		require.NoError(t, cli.List(ctx, list))
		return len(list.Items) == 0
	})
}

// TestControllerFastCompositionUpdates proves that the last write wins i.e. the most recent composition change
// will win when many changes are made in a short period of time.
func TestControllerFastCompositionUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager))
	require.NoError(t, NewExecController(mgr.Manager, minimalTestConfig, &testutil.ExecConn{Hook: func(s *apiv1.Synthesizer) []client.Object {
		// simulate real pods taking some random amount of time to generation
		time.Sleep(time.Millisecond * time.Duration(rand.Int63n(300)))
		return nil
	}}))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Send a bunch of updates in a row
	for i := 0; i < 10; i++ {
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			if client.IgnoreNotFound(err) != nil {
				return err
			}
			comp.Spec.Bindings = []apiv1.Binding{{Key: fmt.Sprintf("test-%d", i), Resource: apiv1.ResourceBinding{Name: "test"}}}
			return cli.Update(ctx, comp)
		})
		require.NoError(t, err)
	}

	// It should eventually converge even though pods did not terminate in order
	latest := comp.Generation
	testutil.Eventually(t, func() bool {
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == latest
	})
}

// TestControllerSwitchingSynthesizers proves that it's possible to switch between otherwise
// unrelated synthesizers.
func TestControllerSwitchingSynthesizers(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewExecController(mgr.Manager, minimalTestConfig, &testutil.ExecConn{
		Hook: func(s *apiv1.Synthesizer) []client.Object {
			cm := &corev1.ConfigMap{}
			cm.APIVersion = "v1"
			cm.Kind = "ConfigMap"
			cm.Name = "test"
			cm.Namespace = "default"

			if s.Name == "test-syn-2" {
				// return two objects for the second test synthesizer, we'll assert on that later
				return []client.Object{cm, cm}
			}

			return []client.Object{cm}
		},
	}))

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager))
	mgr.Start(t)

	syn1 := &apiv1.Synthesizer{}
	syn1.Name = "test-syn-1"
	syn1.Spec.Image = "initial-image"
	syn1.Spec.RolloutCooldown = &metav1.Duration{Duration: time.Millisecond * 10}
	require.NoError(t, cli.Create(ctx, syn1))

	syn2 := &apiv1.Synthesizer{}
	syn2.Name = "test-syn-2"
	syn2.Spec.Image = "updated-image"
	syn1.Spec.RolloutCooldown = &metav1.Duration{Duration: time.Millisecond * 10}
	require.NoError(t, cli.Create(ctx, syn2))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn1.Name
	require.NoError(t, cli.Create(ctx, comp))

	var initialSlices []*apiv1.ResourceSliceRef
	var initialGen int64
	t.Run("initial creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ResourceSlices != nil
		})
		initialSlices = comp.Status.CurrentSynthesis.ResourceSlices
		initialGen = comp.Generation
	})

	t.Run("update synthesizer name", func(t *testing.T) {
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			if err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp); err != nil {
				return err
			}
			comp.Spec.Synthesizer.Name = syn2.Name
			return cli.Update(ctx, comp)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration > initialGen
		})
		assert.NotEqual(t, comp.Status.CurrentSynthesis.ResourceSlices, initialSlices)
	})
}
