package synthesis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

var minimalTestConfig = &Config{
	WrapperImage:    "test-wrapper-image",
	MaxRestarts:     3,
	Timeout:         time.Second * 2,
	RolloutCooldown: time.Millisecond * 10,
}

func TestControllerHappyPath(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	testutil.NewPodController(t, mgr.Manager)
	cli := mgr.GetClient()

	err := NewController(mgr.Manager, minimalTestConfig)
	require.NoError(t, err)
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
	})

	t.Run("composition update", func(t *testing.T) {
		// Updating the composition should cause re-synthesis
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		comp.Spec.Inputs = []apiv1.InputRef{{Name: "anything"}}
		require.NoError(t, cli.Update(ctx, comp))

		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedGeneration == comp.Generation
		})

		// The previous state is retained
		if comp.Status.PreviousState == nil {
			t.Error("state wasn't swapped to previous")
		} else {
			assert.Equal(t, comp.Generation-1, comp.Status.PreviousState.ObservedGeneration)
		}
	})

	t.Run("synthesizer update", func(t *testing.T) {
		syn.Spec.Image = "updated-image"
		require.NoError(t, cli.Update(ctx, syn))

		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedSynthesizerGeneration == syn.Generation
		})

		// The previous state is retained
		if comp.Status.PreviousState == nil {
			t.Error("state wasn't swapped to previous")
		} else {
			assert.Equal(t, comp.Generation-1, comp.Status.PreviousState.ObservedGeneration)
		}
	})
}
