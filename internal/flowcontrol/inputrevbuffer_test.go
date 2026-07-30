package flowcontrol

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// TestInputRevisionWriteBufferDoesNotResurrectConcurrentlyPrunedKey proves the test op
// protects against a second writer to status.inputRevisions itself (e.g. the pruning
// controller in internal/controllers/watch/pruning.go), unlike an unlocked merge patch,
// which would silently resurrect whatever the buffer's stale read still had for the key
// the pruner just removed.
func TestInputRevisionWriteBufferDoesNotResurrectConcurrentlyPrunedKey(t *testing.T) {
	ctx := testutil.NewContext(t)
	raced := false
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			err := c.Get(ctx, key, obj, opts...)
			if _, ok := obj.(*apiv1.Composition); ok && !raced && err == nil {
				raced = true
				// Simulate the pruning controller's own Get -> mutate -> Status().Update
				// removing "baz", interleaved between our Get and our Patch.
				pruned := &apiv1.Composition{}
				require.NoError(t, c.Get(ctx, key, pruned))
				pruned.Status.InputRevisions = []apiv1.InputRevisions{{Key: "bar", ResourceVersion: "1"}}
				require.NoError(t, c.Status().Update(ctx, pruned))
			}
			return err
		},
	})
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)
	comp.Status.InputRevisions = []apiv1.InputRevisions{
		{Key: "bar", ResourceVersion: "1"},
		{Key: "baz", ResourceVersion: "1"},
	}
	require.NoError(t, cli.Status().Update(ctx, comp))

	// The buffer flushes an update to an unrelated key based on its now-stale read, which
	// still includes "baz".
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx) // first attempt: test op fails against the pruner's write, retries
	w.processQueueItem(ctx) // retry against a fresh read: succeeds

	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.ElementsMatch(t, []string{"bar", "foo"}, inputRevisionKeys(comp), "the pruned key must not be resurrected")
	assert.Empty(t, w.state)
}

// TestInputRevisionWriteBufferStaleCacheReadDoesNotRevertPreviousFlush simulates the
// flush-N / flush-N+1 sequence where the informer cache backing w.client.Get lags behind
// flush N's own write: flush N+1 (for a different key) reads a stale pre-flush-N snapshot.
// A JSON merge patch would blindly replace the whole array with that stale view, reverting
// flush N's key; the test op here must instead fail and force a retry against a fresh read.
func TestInputRevisionWriteBufferStaleCacheReadDoesNotRevertPreviousFlush(t *testing.T) {
	ctx := testutil.NewContext(t)
	var stale *apiv1.Composition
	returnStale := false
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if returnStale {
				if comp, ok := obj.(*apiv1.Composition); ok {
					returnStale = false // the buffer's retry then reads a fresh value
					stale.DeepCopyInto(comp)
					return nil
				}
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)
	comp.Status.InputRevisions = []apiv1.InputRevisions{
		{Key: "A", ResourceVersion: "1"},
		{Key: "B", ResourceVersion: "1"},
	}
	require.NoError(t, cli.Status().Update(ctx, comp))
	stale = comp.DeepCopy() // the pre-flush-N snapshot an informer cache would still be serving

	// Flush N: patch A to rv2.
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "A", ResourceVersion: "2"})
	w.processQueueItem(ctx)

	require.NoError(t, cli.Get(ctx, nsn, comp))
	require.Equal(t, "2", revisionFor(comp, "A"), "flush N must have applied")

	// Flush N+1: patch B to rv2, but the buffer's next Get returns the stale pre-flush-N
	// snapshot, as if the informer cache had not caught up with flush N's write yet.
	returnStale = true
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "B", ResourceVersion: "2"})
	w.processQueueItem(ctx) // first attempt against the stale read: test op fails, retries
	w.processQueueItem(ctx) // retry against a fresh read: succeeds

	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.Equal(t, "2", revisionFor(comp, "A"), "A must not revert to rv1")
	assert.Equal(t, "2", revisionFor(comp, "B"), "B must be updated")
}

// TestInputRevisionWriteBufferPatchConflictIsRetried proves the k8serrors.IsConflict branch
// in updateComposition is live: a 409 from the patch call is logged as a conflict and the
// buffered update is retried rather than dropped.
func TestInputRevisionWriteBufferPatchConflictIsRetried(t *testing.T) {
	ctx := testutil.NewContext(t)
	failNext := true
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if failNext {
				failNext = false
				return k8serrors.NewConflict(schema.GroupResource{Group: "eno.azure.io", Resource: "compositions"}, obj.GetName(), errors.New("simulated conflict"))
			}
			return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
		},
	})
	w := NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn := client.ObjectKeyFromObject(comp)

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.processQueueItem(ctx) // rejected with a conflict, buffered update is kept for retry
	w.processQueueItem(ctx) // retry succeeds

	require.NoError(t, cli.Get(ctx, nsn, comp))
	require.Len(t, comp.Status.InputRevisions, 1)
	assert.Equal(t, "foo", comp.Status.InputRevisions[0].Key)
}

func revisionFor(comp *apiv1.Composition, key string) string {
	for _, ir := range comp.Status.InputRevisions {
		if ir.Key == key {
			return ir.ResourceVersion
		}
	}
	return ""
}

// TestInputRevisionWriteBufferConcurrentWrites drives many goroutines that concurrently call
// PatchInputRevisionAsync/RemoveInputRevisionAsync while the buffer's real Start loop flushes
// in the background, and asserts every composition converges to the expected final state.
// Each (composition, key) pair is owned by exactly one goroutine, so there's no ambiguity
// about which write should win - this isolates the assertion from scheduling nondeterminism
// while still exercising the buffer's locking, coalescing, and workqueue under genuine
// concurrency (run with -race).
func TestInputRevisionWriteBufferConcurrentWrites(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)
	go w.Start(ctx)

	const compCount = 6
	const keysPerComp = 4
	const iterations = 40

	nsns := make([]client.ObjectKey, compCount)
	for i := range nsns {
		comp := newTestComposition(t, cli, fmt.Sprintf("test-comp-%d", i))
		nsns[i] = client.ObjectKeyFromObject(comp)
	}

	type finalState struct {
		revision string
		removed  bool
	}
	var mu sync.Mutex
	want := make(map[client.ObjectKey]map[string]finalState, compCount)
	for _, nsn := range nsns {
		want[nsn] = make(map[string]finalState, keysPerComp)
	}

	var wg sync.WaitGroup
	for c := 0; c < compCount; c++ {
		for k := 0; k < keysPerComp; k++ {
			nsn, key := nsns[c], fmt.Sprintf("key-%d", k)
			wg.Add(1)
			go func() {
				defer wg.Done()
				var last finalState
				for i := 0; i < iterations; i++ {
					if rand.IntN(4) == 0 {
						w.RemoveInputRevisionAsync(nsn, key)
						last = finalState{removed: true}
					} else {
						rv := strconv.Itoa(i)
						w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: key, ResourceVersion: rv})
						last = finalState{revision: rv}
					}
					time.Sleep(time.Duration(rand.IntN(2)) * time.Millisecond)
				}
				mu.Lock()
				want[nsn][key] = last
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	// Wait for the buffer to fully drain before asserting the final state.
	testutil.Eventually(t, func() bool {
		w.mut.Lock()
		defer w.mut.Unlock()
		return len(w.state) == 0 && w.queue.Len() == 0
	})

	for _, nsn := range nsns {
		comp := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, nsn, comp))
		for key, exp := range want[nsn] {
			rv := revisionFor(comp, key)
			if exp.removed {
				assert.Empty(t, rv, "key %s on %s should have been removed", key, nsn.Name)
			} else {
				assert.Equal(t, exp.revision, rv, "key %s on %s has wrong final revision", key, nsn.Name)
			}
		}
	}
}
