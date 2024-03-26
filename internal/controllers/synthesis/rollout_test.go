package synthesis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

// TestControllerRollout proves that synthesizer changes are eventually rolled out across their compositions.
func TestControllerRollout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager))
	require.NoError(t, NewExecController(mgr.Manager, minimalTestConfig, &testutil.ExecConn{}))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	syn.Spec.RolloutCooldown = &metav1.Duration{Duration: time.Millisecond * 10}
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	t.Run("initial creation", func(t *testing.T) {
		testutil.Eventually(t, func() bool {
			require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
		})
	})

	t.Run("synthesizer update", func(t *testing.T) {
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
				return err
			}
			syn.Spec.Image = "updated-image"
			return cli.Update(ctx, syn)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
			return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration >= syn.Generation
		})
	})
}

// TestControllerSynthesizerRolloutCooldown proves that the synth rollout cooldown period is honored when
// rolling out changes across compositions.
func TestControllerSynthesizerRolloutCooldown(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewPodLifecycleController(mgr.Manager, minimalTestConfig))
	require.NoError(t, NewStatusController(mgr.Manager))
	require.NoError(t, NewRolloutController(mgr.Manager)) // Rollout should not continue during this test
	require.NoError(t, NewExecController(mgr.Manager, minimalTestConfig, &testutil.ExecConn{}))
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	syn.Spec.RolloutCooldown = &metav1.Duration{Duration: time.Millisecond * 10}
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Wait for initial sync
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// First synthesizer update
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)

	// The first synthesizer update should be applied to the composition
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == syn.Generation
	})

	// Wait for the informer cache to know about the last update
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(syn), syn)))
		return syn.Status.LastRolloutTime != nil
	})

	// Second synthesizer update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)

	// The second synthesizer update should not be applied to the composition because we're within the update window
	time.Sleep(time.Millisecond * 250)
	original := comp.DeepCopy()
	require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
	assert.Equal(t, original.Generation, comp.Generation, "spec hasn't been updated")
}
