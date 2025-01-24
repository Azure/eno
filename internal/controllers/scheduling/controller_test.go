package scheduling

import (
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	debug = true
}

func TestBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID != ""
	})
	initialUUID := comp.Status.CurrentSynthesis.UUID

	// Mark this synthesis as complete
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Update the composition
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Spec.SynthesisEnv = []apiv1.EnvVar{{Name: "foo", Value: "bar"}}
		return cli.Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil &&
			comp.Status.CurrentSynthesis.UUID != initialUUID &&
			comp.Status.PreviousSynthesis != nil && comp.Status.PreviousSynthesis.UUID == initialUUID
	})

	// Remove the current synthesis, things should eventually converge
	updatedUUID := comp.Status.CurrentSynthesis.UUID
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis = nil
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil &&
			comp.Status.CurrentSynthesis != nil &&
			comp.Status.CurrentSynthesis.UUID != initialUUID &&
			comp.Status.CurrentSynthesis.UUID != updatedUUID &&
			comp.Status.PreviousSynthesis != nil && comp.Status.PreviousSynthesis.UUID == initialUUID
	})
}

func TestSynthRolloutBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID != ""
	})
	lastUUID := comp.Status.CurrentSynthesis.UUID

	// Mark this synthesis as complete for the current synth version
	start := time.Now()
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration = synth.Generation
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Modify the synth
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(synth), synth)
		synth.Spec.Command = []string{"new", "value"}
		return cli.Update(ctx, synth)
	})
	require.NoError(t, err)

	// It should eventually resynthesize
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis.UUID != lastUUID
	})
	assert.Less(t, time.Since(start), time.Millisecond*500, "initial deferral period")
	lastUUID = comp.Status.CurrentSynthesis.UUID

	// Mark this synthesis as complete for the current synth version
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration = synth.Generation
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// Modify the synth again
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(synth), synth)
		synth.Spec.Command = []string{"newer", "value"}
		return cli.Update(ctx, synth)
	})
	require.NoError(t, err)

	// It should eventually resynthesize but this time with a cooldown
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis.UUID != lastUUID
	})
	assert.Greater(t, time.Since(start), time.Millisecond*500, "chilled deferral period")
}

func TestDeferredInput(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	synth.Spec.Refs = []apiv1.Ref{{Key: "foo", Defer: true}}
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Spec.Bindings = []apiv1.Binding{{Key: "foo", Resource: apiv1.ResourceBinding{Name: "test-input"}}}
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "bar"}}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID != ""
	})
	lastUUID := comp.Status.CurrentSynthesis.UUID

	// Mark this synthesis as complete but for the wrong input revision
	start := time.Now()
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.CurrentSynthesis.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "NOT bar"}}
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// It should eventually resynthesize
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis.UUID != lastUUID
	})
	assert.Less(t, time.Since(start), time.Millisecond*500, "initial deferral period")
	lastUUID = comp.Status.CurrentSynthesis.UUID

	// One more time
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.CurrentSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.CurrentSynthesis.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "NOT bar"}}
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.CurrentSynthesis.UUID != lastUUID
	})
	assert.Greater(t, time.Since(start), time.Millisecond*500, "chilled deferral period")
}
