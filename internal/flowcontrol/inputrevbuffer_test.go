package flowcontrol

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func newTestComposition(t *testing.T, cli client.Client, name string) *apiv1.Composition {
	t.Helper()
	comp := &apiv1.Composition{}
	comp.Name = name
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "test-synth"
	require.NoError(t, cli.Create(testutil.NewContext(t), comp))
	return comp
}

func TestInputRevisionWriteBufferBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})

	w.processQueueItem(ctx)

	require.NoError(t, cli.Get(ctx, nsn, comp))
	require.Len(t, comp.Status.InputRevisions, 1)
	assert.Equal(t, "foo", comp.Status.InputRevisions[0].Key)
	assert.Equal(t, "1", comp.Status.InputRevisions[0].ResourceVersion)

	// state fully flushed
	assert.Len(t, w.state, 0)
	assert.Equal(t, 0, w.queue.Len())
}

func TestInputRevisionWriteBufferCoalescesMultipleKeys(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	// Three different input keys enqueued before any flush.
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "a", ResourceVersion: "1"})
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "b", ResourceVersion: "1"})
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "c", ResourceVersion: "1"})

	// A single dequeue should write all three at once.
	rvBefore := getResourceVersion(t, cli, nsn)
	w.processQueueItem(ctx)
	rvAfter := getResourceVersion(t, cli, nsn)
	assert.NotEqual(t, rvBefore, rvAfter, "expected exactly one status update")

	require.NoError(t, cli.Get(ctx, nsn, comp))
	keys := inputRevisionKeys(comp)
	assert.ElementsMatch(t, []string{"a", "b", "c"}, keys)
}

func TestInputRevisionWriteBufferLastWriteWinsPerKey(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "2"})
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "3"})

	w.processQueueItem(ctx)

	require.NoError(t, cli.Get(ctx, nsn, comp))
	require.Len(t, comp.Status.InputRevisions, 1)
	assert.Equal(t, "3", comp.Status.InputRevisions[0].ResourceVersion)
}

func TestInputRevisionWriteBufferRemove(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)
	comp.Status.InputRevisions = []apiv1.InputRevisions{
		{Key: "keep", ResourceVersion: "1"},
		{Key: "drop", ResourceVersion: "1"},
	}
	require.NoError(t, cli.Status().Update(ctx, comp))

	w.RemoveInputRevisionAsync(nsn, "drop")
	w.processQueueItem(ctx)

	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.ElementsMatch(t, []string{"keep"}, inputRevisionKeys(comp))
}

func TestInputRevisionWriteBufferSetThenRemoveSameKeyCoalesces(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	// set then remove the same key before any flush: the remove wins.
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.RemoveInputRevisionAsync(nsn, "foo")
	w.processQueueItem(ctx)

	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.Empty(t, comp.Status.InputRevisions)
}

func TestInputRevisionWriteBufferNoOpDoesNotError(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)
	comp.Status.InputRevisions = []apiv1.InputRevisions{{Key: "foo", ResourceVersion: "1"}}
	require.NoError(t, cli.Status().Update(ctx, comp))
	rvBefore := getResourceVersion(t, cli, nsn)

	// Enqueue an identical revision - should be a no-op, no status write.
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx)

	assert.Equal(t, rvBefore, getResourceVersion(t, cli, nsn), "no-op update must not write status")
	assert.Len(t, w.state, 0)
}

func TestInputRevisionWriteBufferDeletedCompositionDropped(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	nsn := client.ObjectKey{Namespace: "default", Name: "does-not-exist"}
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx)

	// Nothing to assert other than the buffer drained cleanly without hanging/erroring.
	assert.Len(t, w.state, 0)
	assert.Equal(t, 0, w.queue.Len())
}

func getResourceVersion(t *testing.T, cli client.Client, nsn client.ObjectKey) string {
	t.Helper()
	comp := &apiv1.Composition{}
	require.NoError(t, cli.Get(testutil.NewContext(t), nsn, comp))
	return comp.ResourceVersion
}

func inputRevisionKeys(comp *apiv1.Composition) []string {
	keys := make([]string, 0, len(comp.Status.InputRevisions))
	for _, ir := range comp.Status.InputRevisions {
		keys = append(keys, ir.Key)
	}
	return keys
}

func TestSetInputRevisions(t *testing.T) {
	tests := []struct {
		name      string
		comp      *apiv1.Composition
		revs      *apiv1.InputRevisions
		expected  bool
		finalRevs []apiv1.InputRevisions
	}{
		{
			name: "add new revision when key is not found",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:      "rev2",
				Revision: ptr.To(2),
			},
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1)},
				{Key: "rev2", Revision: ptr.To(2)},
			},
		},
		{
			name: "update existing revision",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:      "rev1",
				Revision: ptr.To(2),
			},
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(2)},
			},
		},
		{
			name: "no update if revision is identical",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:      "rev1",
				Revision: ptr.To(1),
			},
			expected: false,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1)},
			},
		},
		{
			name: "no update if revision is identical and synth generation is set",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(3))},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:                   "rev1",
				Revision:              ptr.To(1),
				SynthesizerGeneration: ptr.To(int64(3)),
			},
			expected: false,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(3))},
			},
		},
		{
			name: "update if revision is identical but synth generation is not",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(3))},
					},
				},
			},
			revs: &apiv1.InputRevisions{
				Key:                   "rev1",
				Revision:              ptr.To(1),
				SynthesizerGeneration: ptr.To(int64(5)),
			},
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1), SynthesizerGeneration: ptr.To(int64(5))},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := setInputRevisions(tt.comp, tt.revs)
			assert.Equal(t, tt.expected, result)
			assert.Equal(t, tt.finalRevs, tt.comp.Status.InputRevisions)
		})
	}
}

func TestRemoveInputRevision(t *testing.T) {
	tests := []struct {
		name      string
		comp      *apiv1.Composition
		key       string
		expected  bool
		finalRevs []apiv1.InputRevisions
	}{
		{
			name: "remove existing revision",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
						{Key: "rev2", Revision: ptr.To(2)},
					},
				},
			},
			key:      "rev1",
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev2", Revision: ptr.To(2)},
			},
		},
		{
			name: "remove last revision",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			key:       "rev1",
			expected:  true,
			finalRevs: []apiv1.InputRevisions{},
		},
		{
			name: "no removal if key not found",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
					},
				},
			},
			key:      "rev2",
			expected: false,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1)},
			},
		},
		{
			name: "remove from middle of list",
			comp: &apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "rev1", Revision: ptr.To(1)},
						{Key: "rev2", Revision: ptr.To(2)},
						{Key: "rev3", Revision: ptr.To(3)},
					},
				},
			},
			key:      "rev2",
			expected: true,
			finalRevs: []apiv1.InputRevisions{
				{Key: "rev1", Revision: ptr.To(1)},
				{Key: "rev3", Revision: ptr.To(3)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeInputRevision(tt.comp, tt.key)
			assert.Equal(t, tt.expected, result)
			assert.Equal(t, tt.finalRevs, tt.comp.Status.InputRevisions)
		})
	}
}

// TestInputRevisionWriteBufferMapsDoNotLeak asserts that once a composition is created,
// flushed, and deleted, the buffer retains no per-composition bookkeeping - guarding
// against the `queued` map (previously only ever set to false, never deleted) growing
// without bound on a cluster with composition churn.
func TestInputRevisionWriteBufferMapsDoNotLeak(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx) // create + flush

	require.NoError(t, cli.Delete(ctx, comp))
	w.RemoveInputRevisionAsync(nsn, "foo")
	w.processQueueItem(ctx) // composition is gone, buffered update is dropped

	w.mut.Lock()
	defer w.mut.Unlock()
	assert.Empty(t, w.state, "state must not retain an entry once settled")
	assert.Empty(t, w.insertionTime, "insertionTime must not retain an entry once settled")
	assert.Empty(t, w.queued, "queued must not retain an entry once settled")
}

// TestInputRevisionWriteBufferInsertionTimePreservedDuringRace simulates a producer
// enqueuing a new update for the same composition while a flush is still in flight (inside
// the window between reading state and settling). Before the fix, the success path
// unconditionally deleted insertionTime, so the next flush would compute latency against
// the zero time (~62 billion ms). The fix must keep insertionTime intact whenever a
// racing update is still pending.
func TestInputRevisionWriteBufferInsertionTimePreservedDuringRace(t *testing.T) {
	ctx := testutil.NewContext(t)
	var w *CompositionInputRevisionWriteBuffer
	var nsn client.ObjectKey
	raced := false
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			err := c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			if !raced {
				raced = true
				// Simulate a producer racing in a new update while this flush is still
				// in flight, i.e. before the success path decides whether to settle.
				w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "bar", ResourceVersion: "2"})
			}
			return err
		},
	})
	w = NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn = client.ObjectKeyFromObject(comp)

	before := time.Now()
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx) // flushes "foo"; races in "bar" mid-flush

	w.mut.Lock()
	insertionTime := w.insertionTime[nsn]
	w.mut.Unlock()
	require.False(t, insertionTime.IsZero(), "insertionTime must not be cleared while an update is still pending")
	assert.False(t, insertionTime.Before(before), "insertionTime should reflect the racing enqueue, not a stale or zero value")

	latency := time.Since(insertionTime)
	assert.GreaterOrEqual(t, latency, time.Duration(0), "latency must be non-negative")
	assert.Less(t, latency, 10*time.Second, "latency must reflect a real timestamp, not the zero time")

	// The racing update is still buffered and flushes cleanly on the next pass.
	w.processQueueItem(ctx)
	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.ElementsMatch(t, []string{"foo", "bar"}, inputRevisionKeys(comp))
	assert.Empty(t, w.state)
	assert.Empty(t, w.insertionTime)
}

// TestInputRevisionWriteBufferBackoffGrowsUnderSustainedLoad proves that the rate limiter
// backs off exponentially for a composition whose inputs keep churning: Forget must only be
// called once a flush finds nothing pending for that composition.
func TestInputRevisionWriteBufferBackoffGrowsUnderSustainedLoad(t *testing.T) {
	ctx := testutil.NewContext(t)
	var w *CompositionInputRevisionWriteBuffer
	var nsn client.ObjectKey
	const churnCycles = 3
	patchCalls := 0
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			err := c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
			patchCalls++
			if patchCalls <= churnCycles {
				// Simulate another input changing while this flush is still in flight.
				w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: strconv.Itoa(patchCalls + 1)})
			}
			return err
		},
	})
	w = NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn = client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})

	prevRequeues := 0
	for i := 0; i < churnCycles; i++ {
		w.processQueueItem(ctx)
		requeues := w.queue.NumRequeues(nsn)
		assert.Greater(t, requeues, prevRequeues, "rate limit must not be forgotten while the composition is still churning")
		prevRequeues = requeues
	}

	// One more flush finds nothing pending: the composition is idle, so the rate limiter resets.
	w.processQueueItem(ctx)
	assert.Equal(t, 0, w.queue.NumRequeues(nsn), "rate limit should reset once the composition goes idle")
}

// TestInputRevisionWriteBufferBackoffStaysBaseForIsolatedComposition proves that a
// composition with a single, isolated update is not penalized by the exponential backoff -
// it flushes and its rate limiter resets to the fast path.
func TestInputRevisionWriteBufferBackoffStaysBaseForIsolatedComposition(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx)

	assert.Equal(t, 0, w.queue.NumRequeues(nsn), "an isolated update should reset the rate limit to the fast path")
}

// TestInputRevisionWriteBufferPatchDoesNotConflictWithUnrelatedStatusWrite proves that
// scoping the flush to a JSON patch on status.inputRevisions avoids the conflict a full
// Status().Update would hit when another controller concurrently writes a different status
// field (e.g. CurrentSynthesis) between our Get and our write.
func TestInputRevisionWriteBufferPatchDoesNotConflictWithUnrelatedStatusWrite(t *testing.T) {
	ctx := testutil.NewContext(t)
	raced := false
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if _, ok := obj.(*apiv1.Composition); ok && !raced && err == nil {
				raced = true
				// Simulate a concurrent writer (e.g. the synthesis controller) updating an
				// unrelated status field between our Get and our Patch.
				concurrent := &apiv1.Composition{}
				require.NoError(t, c.Get(ctx, key, concurrent))
				concurrent.Status.CurrentSynthesis = &apiv1.Synthesis{UUID: "concurrent-write"}
				require.NoError(t, c.Status().Update(ctx, concurrent))
			}
			return err
		},
	})
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx)

	// The scoped patch succeeded on the first attempt despite the concurrent write.
	assert.Empty(t, w.state)
	assert.Equal(t, 0, w.queue.NumRequeues(nsn))

	require.NoError(t, cli.Get(ctx, nsn, comp))
	require.Len(t, comp.Status.InputRevisions, 1)
	assert.Equal(t, "foo", comp.Status.InputRevisions[0].Key)
	require.NotNil(t, comp.Status.CurrentSynthesis)
	assert.Equal(t, "concurrent-write", comp.Status.CurrentSynthesis.UUID, "unrelated status field must be preserved")
}

