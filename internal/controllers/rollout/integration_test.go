package rollout

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/flowcontrol"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/testutil"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
)

var testSynthesisConfig = &synthesis.Config{
	SliceCreationQPS: 15,
	PodNamespace:     "default",
	ExecutorImage:    "test-image",
}

// TestSynthesizerRollout proves that synthesizer changes are eventually rolled out across their compositions.
func TestSynthesizerRollout(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
	require.NoError(t, NewSynthesizerController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager, time.Millisecond*10))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		return output, nil
	})
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

// TestRolloutIgnoreSideEffects proves that synthesizer changes are not rolled out to compositions which are ignoring side effects.
func TestRolloutIgnoreSideEffects(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
	require.NoError(t, NewSynthesizerController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager, time.Millisecond*10))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		return output, nil
	})
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = syn.Name
	comp.Annotations = map[string]string{
		"eno.azure.io/ignore-side-effects": "true",
	}
	require.NoError(t, cli.Create(ctx, comp))

	// Initial creation.
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	})

	// Update the synthesizer while ignoring side effects.
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)
	// Give some time to the controller to process the change.
	time.Sleep(time.Second)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.NotNil(t, comp.Status.CurrentSynthesis)
	require.Less(t, comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration, syn.Generation)

	// Stop ignoring side effects.
	compCopy := comp.DeepCopy()
	comp.Annotations = map[string]string{
		"eno.azure.io/ignore-side-effects": "false",
	}
	require.NoError(t, cli.Patch(ctx, comp, client.MergeFrom(compCopy)))

	testutil.Eventually(t, func() bool {
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		return comp.Status.CurrentSynthesis != nil && isInSync(comp, syn)
	})

	// Update the synthesizer while honoring side effects.
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "another-updated-image"
		return cli.Update(ctx, syn)
	})

	// This time the rollout is observed.
	testutil.Eventually(t, func() bool {
		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
		return comp.Status.CurrentSynthesis != nil && isInSync(comp, syn)
	})
}

// TestSynthesizerRolloutCooldown proves that the synth rollout cooldown period is honored when
// rolling out changes across compositions.
func TestSynthesizerRolloutCooldown(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
	require.NoError(t, NewSynthesizerController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager, time.Hour))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		return output, nil
	})
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

// TestSynthesizerRolloutCooldown proves that the synth rollout controller honors
// input revisions and does not re-synthesize a composition if its inputs are
// not in lockstep.
func TestSynthesizerRolloutInputs(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
	require.NoError(t, NewSynthesizerController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager, time.Hour))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		return output, nil
	})
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

	copyForStatus := comp.DeepCopy()
	copyForStatus.Status.InputRevisions = []apiv1.InputRevisions{
		{
			Key:      "some-input",
			Revision: ptr.To(1),
		},
		{
			Key:      "some-other-input",
			Revision: ptr.To(2),
		},
	}
	require.NoError(t, cli.Status().Patch(ctx, copyForStatus, client.MergeFrom(comp)))

	// First synthesizer update
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)

	// The synthesizer update should not be applied to the composition because inputs are not in lockstep.
	time.Sleep(time.Millisecond * 250)
	require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
	assert.Equal(t, int64(1), comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration, "composition has not been resynthesized")

	copyForStatus = comp.DeepCopy()
	copyForStatus.Status.InputRevisions = []apiv1.InputRevisions{
		{
			Key:      "some-input",
			Revision: ptr.To(2),
		},
		{
			Key:      "some-other-input",
			Revision: ptr.To(2),
		},
	}
	require.NoError(t, cli.Status().Patch(ctx, copyForStatus, client.MergeFrom(comp)))
	// The synthesizer update should be applied to the composition after the inputs reach lockstep.
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration == int64(2)
	})
}

// TestSynthesizerRolloutDeleted proves that compositions will not be updated while deleting.
func TestSynthesizerRolloutDeleted(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()

	require.NoError(t, flowcontrol.NewSynthesisConcurrencyLimiter(mgr.Manager, 10, 0))
	require.NoError(t, NewSynthesizerController(mgr.Manager))
	require.NoError(t, NewController(mgr.Manager, time.Millisecond*10))
	require.NoError(t, synthesis.NewPodLifecycleController(mgr.Manager, testSynthesisConfig))
	testutil.WithFakeExecutor(t, mgr, func(ctx context.Context, s *apiv1.Synthesizer, input *krmv1.ResourceList) (*krmv1.ResourceList, error) {
		output := &krmv1.ResourceList{}
		return output, nil
	})
	mgr.Start(t)

	syn := &apiv1.Synthesizer{}
	syn.Name = "test-syn"
	syn.Spec.Image = "test-syn-image"
	require.NoError(t, cli.Create(ctx, syn))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"foo"}
	comp.Spec.Synthesizer.Name = syn.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Creation
	testutil.Eventually(t, func() bool {
		require.NoError(t, client.IgnoreNotFound(cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)))
		return comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil
	})

	// Start deletion
	require.NoError(t, cli.Delete(ctx, comp))

	// Update the synthesizer
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(syn), syn); err != nil {
			return err
		}
		syn.Spec.Image = "updated-image"
		return cli.Update(ctx, syn)
	})
	require.NoError(t, err)

	// Wait a bit and prove the composition wasn't updated
	time.Sleep(time.Millisecond * 200)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.Equal(t, int64(2), comp.Generation)
}
