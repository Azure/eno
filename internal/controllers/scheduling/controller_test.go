package scheduling

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
	prometheustestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TestBasics proves that the controller will synthesize a composition when it is created,
// and resynthetize it when the composition is updated.
func TestBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100, 2*time.Second, 0))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != ""
	})
	initialUUID := comp.Status.InFlightSynthesis.UUID

	// Mark this synthesis as complete
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
		comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
		comp.Status.InFlightSynthesis = nil
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
			comp.Status.InFlightSynthesis != nil &&
			comp.Status.InFlightSynthesis.UUID != initialUUID
	})

	// Remove the current synthesis, things should eventually converge
	updatedUUID := comp.Status.InFlightSynthesis.UUID
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis = nil
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil &&
			comp.Status.InFlightSynthesis != nil &&
			comp.Status.InFlightSynthesis.UUID != initialUUID &&
			comp.Status.InFlightSynthesis.UUID != updatedUUID
	})
}

// TestSynthRolloutBasics proves that synthesizer changes cause resynthesis subject to the global cooldown period.
func TestSynthRolloutBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100, 2*time.Second, 0))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != ""
	})
	lastUUID := comp.Status.InFlightSynthesis.UUID

	// Mark this synthesis as complete for the current synth version
	start := time.Now()
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.InFlightSynthesis.ObservedSynthesizerGeneration = synth.Generation
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
		comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
		comp.Status.InFlightSynthesis = nil
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

	// It should resynthesize immediately
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != lastUUID
	})
	assert.Less(t, time.Since(start), time.Millisecond*500, "initial deferral period")
	lastUUID = comp.Status.InFlightSynthesis.UUID

	// Mark this synthesis as complete for the current synth version
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.InFlightSynthesis.ObservedSynthesizerGeneration = synth.Generation
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
		comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
		comp.Status.InFlightSynthesis = nil
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
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != lastUUID
	})
	assert.Greater(t, time.Since(start), time.Millisecond*500, "chilled deferral period")

	// Create a new composition with the same synth - it shouldn't be subject to the cooldown
	start = time.Now()
	comp2 := &apiv1.Composition{}
	comp2.Name = "test-comp-2"
	comp2.Namespace = "default"
	comp2.Finalizers = []string{"eno.azure.io/cleanup"}
	comp2.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp2))

	testutil.Eventually(t, func() bool {
		cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2)
		return comp2.Synthesizing()
	})
	assert.Less(t, time.Since(start), time.Second)
}

// TestDeferredInput proves that changes to deferred inputs cause resynthesis subject to the global cooldown period.
func TestDeferredInput(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100, 2*time.Second, 0))
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
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Spec.Bindings = []apiv1.Binding{{Key: "foo", Resource: apiv1.ResourceBinding{Name: "test-input"}}}
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "bar"}}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != ""
	})
	lastUUID := comp.Status.InFlightSynthesis.UUID

	// Mark this synthesis as complete but for the wrong input revision
	start := time.Now()
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.InFlightSynthesis.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "NOT bar"}}
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
		comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
		comp.Status.InFlightSynthesis = nil
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// It should eventually resynthesize
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != lastUUID
	})
	assert.Less(t, time.Since(start), time.Millisecond*500, "initial deferral period")
	lastUUID = comp.Status.InFlightSynthesis.UUID

	// One more time
	err = retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis.Synthesized = ptr.To(metav1.Now())
		comp.Status.InFlightSynthesis.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "NOT bar"}}
		comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
		comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
		comp.Status.InFlightSynthesis = nil
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != lastUUID
	})
	assert.Greater(t, time.Since(start), time.Millisecond*500, "chilled deferral period")
}

// TestForcedResynth proves that the controller will resynthesize a composition when the forced resynthesis annotation is set.
func TestForcedResynth(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100, 2*time.Second, 0))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Initial synthesis
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != ""
	})
	initialUUID := comp.Status.InFlightSynthesis.UUID

	// Set the forced resynthesis annotation
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.ForceResynthesis()
		return cli.Update(ctx, comp)
	})
	require.NoError(t, err)

	// It should eventually resynthesize
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis.UUID != initialUUID
	})
}

// TestChaos proves that the controller will eventually synthesize a large number of compositions subject to the global concurrency limit.
func TestChaos(t *testing.T) {
	t.Run("one leader", func(t *testing.T) {
		mgr := testutil.NewManager(t)
		require.NoError(t, NewController(mgr.Manager, 5, time.Second, 0))
		mgr.Start(t)

		testChaos(t, mgr)
	})

	// Run the same test but with another controller competing for the same resources
	t.Run("zombie leader", func(t *testing.T) {
		mgr := testutil.NewManager(t)
		require.NoError(t, NewController(mgr.Manager, 5, time.Second, 0))
		require.NoError(t, NewController(mgr.Manager, 5, time.Second, 0))
		mgr.Start(t)

		testChaos(t, mgr)
	})
}

func testChaos(t *testing.T, mgr *testutil.Manager) {
	ctx := testutil.NewContext(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	// Asynchronously mark syntheses as complete
	ctrl.NewControllerManagedBy(mgr.Manager).
		Named("synthCompleter").
		For(&apiv1.Composition{}).
		Complete(reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			comp := &apiv1.Composition{}
			if err := cli.Get(ctx, req.NamespacedName, comp); err != nil {
				return reconcile.Result{}, err
			}

			if !comp.Synthesizing() {
				return reconcile.Result{}, nil
			}

			time.Sleep(time.Duration(rand.IntN(100)) * time.Millisecond)

			comp.Status.InFlightSynthesis.Synthesized = ptr.To(metav1.Now())
			comp.Status.InFlightSynthesis.ObservedSynthesizerGeneration = synth.Generation

			comp.Status.PreviousSynthesis = comp.Status.CurrentSynthesis
			comp.Status.CurrentSynthesis = comp.Status.InFlightSynthesis
			comp.Status.InFlightSynthesis = nil
			return reconcile.Result{}, cli.Status().Update(ctx, comp)
		}))

	// Create all of the test compositions
	const n = 150
	go func() {
		for i := 0; i < n; i++ {
			comp := &apiv1.Composition{}
			comp.Name = fmt.Sprintf("test-comp-%d", i)
			comp.Namespace = "default"
			comp.Finalizers = []string{"eno.azure.io/cleanup"}
			comp.Spec.Synthesizer.Name = synth.Name
			require.NoError(t, cli.Create(ctx, comp))

			time.Sleep(time.Duration(rand.IntN(50)) * time.Millisecond)
		}
	}()

	// Wait for all compositions to be synthesized
	testutil.Eventually(t, func() bool {
		list := &apiv1.CompositionList{}
		require.NoError(t, cli.List(ctx, list))

		var synthesizing int
		for _, comp := range list.Items {
			if comp.Synthesizing() {
				synthesizing++
				assert.False(t, comp.Status.InFlightSynthesis.Deferred)
			}
		}

		assert.Less(t, synthesizing, 8, "concurrency limit")
		return synthesizing == 0 && len(list.Items) == n
	})

	// Update the synthesizer and confirm that the change is eventually applied to at least a few compositions constrained by the cooldown period
	// Note that the cooldown period is 1s because that's the precision of the timestamps
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(synth), synth)
		synth.Spec.Command = []string{"new", "value"}
		return cli.Update(ctx, synth)
	})
	require.NoError(t, err)

	var dispatchTimes []time.Time
	testutil.Eventually(t, func() bool {
		dispatchTimes = nil

		list := &apiv1.CompositionList{}
		require.NoError(t, cli.List(ctx, list))
		for _, comp := range list.Items {
			if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Deferred {
				dispatchTimes = append(dispatchTimes, comp.Status.CurrentSynthesis.Initialized.Time)
			}
		}

		return len(dispatchTimes) > 5
	})

	// Prove that deferred dispatch honors the cooldown period
	sort.Slice(dispatchTimes, func(i, j int) bool { return dispatchTimes[i].Before(dispatchTimes[j]) })
	for i := 1; i < len(dispatchTimes); i++ {
		assert.GreaterOrEqual(t, dispatchTimes[i].Sub(dispatchTimes[i-1]), time.Second, "cooldown period")
	}
}

// TestSerializationGracePeriod proves that the controller eventually gives up when waiting for its previous operation to hit the cache.
// This is important to cover cases where other controllers touch the synthesis UUID (although that shouldn't happen).
func TestSerializationGracePeriod(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	c := &controller{client: cli, concurrencyLimit: 2, cacheGracePeriod: time.Millisecond * 100}

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp-1"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Generation = 2
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "foo", ObservedCompositionGeneration: 1, Synthesized: ptr.To(metav1.Now())}

	comp2 := comp.DeepCopy()
	comp2.Name = "test-comp-2"
	require.NoError(t, cli.Create(ctx, comp))
	require.NoError(t, cli.Create(ctx, comp2))

	require.NoError(t, cli.Status().Update(ctx, comp))
	require.NoError(t, cli.Status().Update(ctx, comp2))

	// Dispatch one of the syntheses
	res, err := c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	assert.False(t, res.Requeue)

	// Modify its synthesis uuid such that it no longer matches the controller's last known op
	require.NoError(t, cli.Status().Patch(ctx, comp, client.RawPatch(types.JSONPatchType, []byte(`[{ "op": "replace", "path": "/status/inFlightSynthesis/uuid", "value": "bar" }]`))))

	// The controller hasn't seen its latest update, so it won't dispatch another synthesis
	res, err = c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)
	assert.NotZero(t, res.RequeueAfter)
	assert.False(t, res.Requeue)

	// After the grace period, the controller will progress
	time.Sleep(time.Millisecond * 100)
	res, err = c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
	assert.False(t, res.Requeue)
}

// TestDispatchOrder proves that operations are dispatched by the controller in the expected order.
func TestDispatchOrder(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	c := &controller{client: cli, concurrencyLimit: 2, cacheGracePeriod: time.Millisecond}

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	synth.Generation = 2
	require.NoError(t, cli.Create(ctx, synth))

	// Waiting for the new synth
	comp := &apiv1.Composition{}
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Generation = 2
	comp.Spec.Synthesizer.Name = synth.Name
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID:                          "foo",
		ObservedCompositionGeneration: comp.Generation,
		ObservedSynthesizerGeneration: synth.Generation,
		Synthesized:                   ptr.To(metav1.Now()),
	}

	// comp1 is ready for resynthesis because its spec has changed since its last synthesis
	comp1 := comp.DeepCopy()
	comp1.Name = "test-comp-1"
	comp1.Status.CurrentSynthesis.ObservedCompositionGeneration--
	require.NoError(t, cli.Create(ctx, comp1))
	require.NoError(t, cli.Status().Update(ctx, comp1))

	// comp2 is ready for synthesis because its synthesizer has changed since its last synthesis
	comp2 := comp.DeepCopy()
	comp2.Name = "test-comp-2"
	comp2.Status.CurrentSynthesis.ObservedSynthesizerGeneration--
	require.NoError(t, cli.Create(ctx, comp2))
	require.NoError(t, cli.Status().Update(ctx, comp2))

	// Dispatch a synthesis - it should be comp1 because composition changes have a higher priority than synthesizer changes
	_, err := c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp1), comp1))
	assert.True(t, comp1.Synthesizing())

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2))
	assert.False(t, comp2.Synthesizing())

	// Prep comp2 for dispatch - serialize the synthesizer rollout into "composition time"
	_, err = c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp1), comp1))
	assert.True(t, comp1.Synthesizing())

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2))
	assert.False(t, comp2.Synthesizing())

	// Dispatch another synthesis - it should be comp2
	_, err = c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp1), comp1))
	assert.True(t, comp1.Synthesizing())

	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp2), comp2))
	assert.True(t, comp2.Synthesizing())
}

// TestSynthOrdering proves strict ordering - stale composition informers should not
// result in deferred syntheses jumping the line. Without special accommodation this
// would not be the case since ordering isn't guaranteed across multiple informers.
func TestSynthOrdering(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	c := &controller{client: cli, concurrencyLimit: 1}

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Generation = 2
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Generation = 2
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	comp.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "foo", ObservedCompositionGeneration: comp.Generation, ObservedSynthesizerGeneration: synth.Generation - 1, Synthesized: ptr.To(metav1.Now())}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// The synthesizer has changed but should not be rolled out (yet)
	c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	assert.False(t, comp.Synthesizing())

	// The composition is updated
	comp.Generation++ // fake client will let us do this
	require.NoError(t, cli.Update(ctx, comp))

	// The next tick will dispatch the composition change, not the synthesizer
	c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(comp), comp))
	require.True(t, comp.Synthesizing())
}

func TestIndexSynthesizersEpoch(t *testing.T) {
	synths := []apiv1.Synthesizer{
		{ObjectMeta: metav1.ObjectMeta{Name: "a", Generation: 0}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b", Generation: 1}},
		{ObjectMeta: metav1.ObjectMeta{Name: "c", Generation: 2}},
	}
	_, a := indexSynthesizers(synths)
	_, b := indexSynthesizers(synths)
	assert.Equal(t, a, b)

	synths[1].Generation++
	_, c := indexSynthesizers(synths)
	assert.NotEqual(t, a, c)

	swap := synths[0]
	synths[0] = synths[1]
	synths[1] = swap
	_, d := indexSynthesizers(synths)
	assert.Equal(t, c, d)

	synths = synths[:2]
	_, e := indexSynthesizers(synths)
	assert.NotEqual(t, d, e)
}

// TestRetries proves that canceled syntheses will be retried eventually.
func TestRetries(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 100, 2*time.Second, 0))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != ""
	})

	// Mark this synthesis as canceled and wait for the retry a few times
	timings := []time.Duration{}
	last := time.Time{}
	for range 3 {
		var canceledUUID string
		err := retry.RetryOnConflict(testutil.Backoff, func() error {
			cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			// metav1.Time has a precision of 1s when marshaled to JSON, so sadly this test takes a few seconds
			comp.Status.InFlightSynthesis.Canceled = ptr.To(metav1.NewTime(time.Now().Add(time.Second)))
			canceledUUID = comp.Status.InFlightSynthesis.UUID
			return cli.Status().Update(ctx, comp)
		})
		require.NoError(t, err)

		testutil.Eventually(t, func() bool {
			err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
			return err == nil &&
				comp.Status.InFlightSynthesis != nil &&
				comp.Status.InFlightSynthesis.UUID != canceledUUID
		})

		now := time.Now()
		if !last.IsZero() {
			timings = append(timings, now.Sub(last))
		}
		last = now
	}

	assert.Greater(t, timings[1], timings[0]+time.Second)
}

// TestRetryContention proves that a new composition will eventually be synthesized even though the
// concurrency limit has been reached by compositions that will never succeed.
func TestRetryContention(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	require.NoError(t, NewController(mgr.Manager, 1, 2*time.Second, 0))
	mgr.Start(t)
	cli := mgr.GetClient()

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	synth.Namespace = "default"
	require.NoError(t, cli.Create(ctx, synth))

	comp := &apiv1.Composition{}
	comp.Name = "test-stuck-comp"
	comp.Namespace = "default"
	comp.Finalizers = []string{"eno.azure.io/cleanup"}
	comp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, comp))

	// Wait for the concurrency limit to be reached
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		return err == nil && comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID != ""
	})

	// Create another composition
	secondComp := &apiv1.Composition{}
	secondComp.Name = "test-second-comp"
	secondComp.Namespace = "default"
	secondComp.Finalizers = []string{"eno.azure.io/cleanup"}
	secondComp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, secondComp))

	// Mark the active synthesis as canceled with a retry time past test timeout
	err := retry.RetryOnConflict(testutil.Backoff, func() error {
		cli.Get(ctx, client.ObjectKeyFromObject(comp), comp)
		comp.Status.InFlightSynthesis.Canceled = ptr.To(metav1.NewTime(time.Now().Add(time.Hour)))
		return cli.Status().Update(ctx, comp)
	})
	require.NoError(t, err)

	// The new composition should eventually be synthesized
	testutil.Eventually(t, func() bool {
		err := cli.Get(ctx, client.ObjectKeyFromObject(secondComp), secondComp)
		return err == nil && secondComp.Status.InFlightSynthesis != nil
	})
}

// TestCompositionHealthMetrics proves that the compositionHealth metric is set correctly during reconciliation.
func TestCompositionHealthMetrics(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)

	// Use a short watchdog threshold so we can test stuck detection
	c := &controller{client: cli, concurrencyLimit: 10, watchdogThreshold: time.Millisecond * 100}

	synth := &apiv1.Synthesizer{}
	synth.Name = "test-synth"
	require.NoError(t, cli.Create(ctx, synth))

	// Create a healthy composition (recently reconciled)
	healthyComp := &apiv1.Composition{}
	healthyComp.Name = "healthy-comp"
	healthyComp.Namespace = "default"
	healthyComp.Finalizers = []string{"eno.azure.io/cleanup"}
	healthyComp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, healthyComp))

	healthyComp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID:       "healthy-uuid",
		Reconciled: ptr.To(metav1.Now()),
	}
	require.NoError(t, cli.Status().Update(ctx, healthyComp))

	// Create a stuck composition (initialized long ago, not reconciled)
	stuckComp := &apiv1.Composition{}
	stuckComp.Name = "stuck-comp"
	stuckComp.Namespace = "default"
	stuckComp.Finalizers = []string{"eno.azure.io/cleanup"}
	stuckComp.Spec.Synthesizer.Name = synth.Name
	require.NoError(t, cli.Create(ctx, stuckComp))

	stuckComp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID:        "stuck-uuid",
		Initialized: ptr.To(metav1.NewTime(time.Now().Add(-time.Hour))), // initialized long ago
	}
	require.NoError(t, cli.Status().Update(ctx, stuckComp))

	// Run reconciliation
	_, err := c.Reconcile(ctx, ctrl.Request{})
	require.NoError(t, err)

	// Verify metrics
	healthyValue := prometheustestutil.ToFloat64(compositionHealth.WithLabelValues("healthy-comp", "default", "test-synth"))
	assert.Equal(t, float64(0), healthyValue, "healthy composition should have health value 0")

	stuckValue := prometheustestutil.ToFloat64(compositionHealth.WithLabelValues("stuck-comp", "default", "test-synth"))
	assert.Equal(t, float64(1), stuckValue, "stuck composition should have health value 1")
}
