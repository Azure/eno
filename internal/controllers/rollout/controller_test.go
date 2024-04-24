package rollout

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
)

var testSynthesisConfig = &synthesis.Config{
	SliceCreationQPS: 15,
	PodNamespace:     "default",
}

// TestSynthesizerRollout proves that synthesizer changes are eventually rolled out across their compositions.
func TestSynthesizerRollout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewController(mgr.Manager, time.Millisecond*10))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, testSynthesisConfig, &testutil.ExecConn{}))
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

// TestSynthesizerRolloutCooldown proves that the synth rollout cooldown period is honored when
// rolling out changes across compositions.
func TestSynthesizerRolloutCooldown(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, NewController(mgr.Manager, time.Hour))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	require.NoError(t, synthesis.NewStatusController(mgr.Manager))
	require.NoError(t, synthesis.NewExecController(mgr.Manager, testSynthesisConfig, &testutil.ExecConn{}))
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

	// Second synthesizer update
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "another-updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)

	// The second synthesizer update should not be applied to the composition because we're within the update window
	time.Sleep(time.Millisecond * 250)
	original := comp.DeepCopy()
	require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
	assert.Equal(t, original.Status.CurrentSynthesis.ObservedSynthesizerGeneration, comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration, "composition has not been resynthesized")
}
