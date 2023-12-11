package synthesis

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

var minimalTestConfig = &Config{
	WrapperImage: "test-wrapper-image",
	MaxRestarts:  3,
	Timeout:      time.Second * 2,
}

func TestControllerHappyPath(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	testutil.NewPodController(t, mgr.Manager, nil)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager, time.Millisecond*10))
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
		// It creates a pod to synthesize the composition
		testutil.Eventually(t, func() bool {
			list := &corev1.PodList{}
			require.NoError(t, cli.List(ctx, list))
			return len(list.Items) > 0
		})

		// The pod eventually completes and is deleted
		testutil.Eventually(t, func() bool {
			list := &corev1.PodList{}
			require.NoError(t, cli.List(ctx, list))
			return len(list.Items) == 0
		})

		// The pod eventually writes a resource slice count to the status
		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ResourceSliceCount != nil
		})
	})

	t.Run("composition update", func(t *testing.T) {
		// Updating the composition should cause re-synthesis
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			comp.Spec.Inputs = []apiv1.InputRef{{Name: "anything"}}
			return cli.Update(ctx, comp)
		})
		require.NoError(t, err)

		latest := comp.Generation
		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedCompositionGeneration == latest
		})

		// The previous state is retained
		if comp.Status.PreviousState == nil {
			t.Error("state wasn't swapped to previous")
		} else {
			assert.Equal(t, comp.Generation-1, comp.Status.PreviousState.ObservedCompositionGeneration)
		}
	})

	t.Run("synthesizer update", func(t *testing.T) {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
				return err
			}
			syn.Spec.Image = "updated-image"
			return cli.Update(ctx, syn)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
		})

		// The previous state is retained
		if comp.Status.PreviousState == nil {
			t.Error("state wasn't swapped to previous")
		} else {
			assert.Equal(t, syn.Generation-1, comp.Status.PreviousState.ObservedSynthesizerGeneration)
		}
	})

	// The pod eventually completes and is deleted
	testutil.Eventually(t, func() bool {
		list := &corev1.PodList{}
		require.NoError(t, cli.List(ctx, list))
		return len(list.Items) == 0
	})
}

func TestControllerFastCompositionUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()
	testutil.NewPodController(t, mgr.Manager, func(c *apiv1.Composition, s *apiv1.Synthesizer) []*apiv1.ResourceSlice {
		// simulate real pods taking some random amount of time to generation
		time.Sleep(time.Millisecond * time.Duration(rand.Int63n(300)))
		return nil
	})

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager, time.Millisecond*10))
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
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			if client.IgnoreNotFound(err) != nil {
				return err
			}
			comp.Spec.Inputs = []apiv1.InputRef{{
				Name: fmt.Sprintf("some-unique-value-%d", i),
			}}
			return cli.Update(ctx, comp)
		})
		require.NoError(t, err)
	}

	// It should eventually converge even though pods did not terminate in order (due to jitter in testutil.NewPodController)
	latest := comp.Generation
	testutil.Eventually(t, func() bool {
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		return comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedCompositionGeneration == latest
	})
}

func TestControllerSynthesizerRollout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	testutil.NewPodController(t, mgr.Manager, nil)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager, time.Hour*24)) // Rollout should not continue during this test
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp1 := &apiv1.Composition{}
	comp1.Name = "test-comp-1"
	comp1.Namespace = "default"
	comp1.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp1))

	comp2 := &apiv1.Composition{}
	comp2.Name = "test-comp-2"
	comp2.Namespace = "default"
	comp2.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp2))

	// Wait for initial sync
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp1), comp1)))
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2)))
		inSync1 := comp1.Status.CurrentState != nil && comp1.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
		inSync2 := comp2.Status.CurrentState != nil && comp2.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
		return inSync1 && inSync2
	})

	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)

	// One of the compositions should be updated but not the other because we set a RolloutCooldown of 1hr
	assertRolloutPending := func() {
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp1), comp1)))
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2)))
			inSync1 := comp1.Status.CurrentState != nil && comp1.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
			inSync2 := comp2.Status.CurrentState != nil && comp2.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
			return (inSync1 && !inSync2) || (!inSync1 && inSync2)
		})
	}

	// Make sure the state persists
	assertRolloutPending()
	time.Sleep(time.Millisecond * 50)
	assertRolloutPending()
}

func TestControllerSwitchingSynthesizers(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()
	testutil.NewPodController(t, mgr.Manager, func(c *apiv1.Composition, s *apiv1.Synthesizer) []*apiv1.ResourceSlice {
		emptySlice := &apiv1.ResourceSlice{}
		emptySlice.GenerateName = "test-"
		emptySlice.Namespace = "default"

		// return two slices for the second test synthesizer, we'll assert on that later
		if s.Name == "test-syn-2" {
			return []*apiv1.ResourceSlice{emptySlice.DeepCopy(), emptySlice.DeepCopy()}
		}
		return []*apiv1.ResourceSlice{emptySlice.DeepCopy()}
	})

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager, time.Millisecond*10))
	mgr.Start(t)

	syn1 := &apiv1.Synthesizer{}
	syn1.Name = "test-syn-1"
	syn1.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn1))

	syn2 := &apiv1.Synthesizer{}
	syn2.Name = "test-syn-2"
	syn2.Spec.Image = "initial-image"
	require.NoError(t, cli.Create(ctx, syn2))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn1.Name
	require.NoError(t, cli.Create(ctx, comp))

	t.Run("initial creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ResourceSliceCount != nil && *comp.Status.CurrentState.ResourceSliceCount == 1
		})
	})

	t.Run("update synthesizer name", func(t *testing.T) {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			if err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp); err != nil {
				return err
			}
			comp.Spec.Synthesizer.Name = syn2.Name
			return cli.Update(ctx, comp)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ResourceSliceCount != nil && *comp.Status.CurrentState.ResourceSliceCount == 2
		})
	})
}
