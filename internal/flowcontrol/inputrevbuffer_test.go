package flowcontrol

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
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

// TestInputRevisionWriteBufferMapsDoNotLeak asserts no per-composition bookkeeping survives once a composition is created, flushed, and deleted.
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

// TestInputRevisionWriteBufferInsertionTimePreservedDuringRace asserts insertionTime survives a producer racing in a new update mid-flush, instead of resetting to the zero time.
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

// TestInputRevisionWriteBufferBackoffGrowsUnderSustainedLoad asserts the rate limiter backs off exponentially while a composition keeps churning.
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

// TestInputRevisionWriteBufferBackoffStaysBaseForIsolatedComposition asserts a single, isolated update resets the rate limiter to the fast path.
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

func TestInputRevisionWriteBufferRequeuesFailedFlushWithoutClobberingNewerUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	var w *CompositionInputRevisionWriteBuffer
	var nsn client.ObjectKey
	patchCalls := 0
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls++
			if patchCalls == 1 {
				w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "2"})
				w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "baz", ResourceVersion: "1"})
				return errors.New("transient patch failure")
			}
			return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
		},
	})
	w = NewCompositionInputRevisionWriteBuffer(cli)

	comp := newTestComposition(t, cli, "test-comp-1")
	nsn = client.ObjectKeyFromObject(comp)
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "bar", ResourceVersion: "1"})

	w.processQueueItem(ctx)

	w.mut.Lock()
	pending := w.state[nsn]
	queued := w.queued[nsn]
	w.mut.Unlock()
	require.Len(t, pending, 3)
	assert.Equal(t, "2", pending["foo"].revs.ResourceVersion, "the newer update must win")
	assert.Equal(t, "1", pending["bar"].revs.ResourceVersion, "the failed update must be restored")
	assert.Equal(t, "1", pending["baz"].revs.ResourceVersion, "the concurrent update must be preserved")
	assert.True(t, queued, "the composition must remain queued after a failed flush")
	assert.Greater(t, w.queue.NumRequeues(nsn), 0)

	w.processQueueItem(ctx)

	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.Equal(t, "2", revisionFor(comp, "foo"))
	assert.Equal(t, "1", revisionFor(comp, "bar"))
	assert.Equal(t, "1", revisionFor(comp, "baz"))
	assert.Empty(t, w.state)
	assert.False(t, w.queued[nsn])
	assert.Equal(t, 2, patchCalls)
}

func TestInputRevisionWriteBufferPatchErrors(t *testing.T) {
	resource := schema.GroupResource{Group: apiv1.SchemeGroupVersion.Group, Resource: "compositions"}
	tests := []struct {
		name      string
		patchErr  error
		wantRetry bool
	}{
		{
			name:     "not found drops update",
			patchErr: k8serrors.NewNotFound(resource, "test-comp-1"),
		},
		{
			name:      "conflict retries update",
			patchErr:  k8serrors.NewConflict(resource, "test-comp-1", errors.New("conflict")),
			wantRetry: true,
		},
		{
			name:      "generic error retries update",
			patchErr:  errors.New("transient patch failure"),
			wantRetry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t)
			patchCalls := 0
			cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
				SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
					patchCalls++
					if patchCalls == 1 {
						return tt.patchErr
					}
					return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
				},
			})
			w := NewCompositionInputRevisionWriteBuffer(cli)

			comp := newTestComposition(t, cli, "test-comp-1")
			nsn := client.ObjectKeyFromObject(comp)
			w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
			w.processQueueItem(ctx)

			if !tt.wantRetry {
				assert.Empty(t, w.state)
				assert.False(t, w.queued[nsn])
				assert.Equal(t, 0, w.queue.NumRequeues(nsn))
				assert.Equal(t, 1, patchCalls)
				require.NoError(t, cli.Get(ctx, nsn, comp))
				assert.Empty(t, comp.Status.InputRevisions)
				return
			}

			require.Len(t, w.state[nsn], 1)
			assert.True(t, w.queued[nsn])
			assert.Greater(t, w.queue.NumRequeues(nsn), 0)

			w.processQueueItem(ctx)

			require.NoError(t, cli.Get(ctx, nsn, comp))
			assert.Equal(t, "1", revisionFor(comp, "foo"))
			assert.Empty(t, w.state)
			assert.False(t, w.queued[nsn])
			assert.Equal(t, 2, patchCalls)
		})
	}
}

// TestInputRevisionWriteBufferRetriesAfterConflictWithUnrelatedStatusWrite asserts a concurrent write to an unrelated status field conflicts, retries, and is preserved rather than clobbered.
func TestInputRevisionWriteBufferRetriesAfterConflictWithUnrelatedStatusWrite(t *testing.T) {
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
	w.processQueueItem(ctx) // our resourceVersion is now stale, conflicts, retries
	w.processQueueItem(ctx) // retry against a fresh read: succeeds

	assert.Empty(t, w.state)
	assert.Equal(t, 0, w.queue.NumRequeues(nsn))

	require.NoError(t, cli.Get(ctx, nsn, comp))
	require.Len(t, comp.Status.InputRevisions, 1)
	assert.Equal(t, "foo", comp.Status.InputRevisions[0].Key)
	require.NotNil(t, comp.Status.CurrentSynthesis)

	assert.Equal(t, "concurrent-write", comp.Status.CurrentSynthesis.UUID, "unrelated status field must be preserved")
}

// TestInputRevisionWriteBufferDoesNotResurrectConcurrentlyPrunedKey asserts a concurrent prune (internal/controllers/watch/pruning.go) of a different key is not resurrected by a racing flush.
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
	w.processQueueItem(ctx) // first attempt: resourceVersion is stale, pruner's write conflicts, retries
	w.processQueueItem(ctx) // retry against a fresh read: succeeds

	require.NoError(t, cli.Get(ctx, nsn, comp))
	assert.ElementsMatch(t, []string{"bar", "foo"}, inputRevisionKeys(comp), "the pruned key must not be resurrected")
	assert.Empty(t, w.state)
}

func revisionFor(comp *apiv1.Composition, key string) string {
	for _, ir := range comp.Status.InputRevisions {
		if ir.Key == key {
			return ir.ResourceVersion
		}
	}
	return ""
}

// TestInputRevisionWriteBufferConcurrentWrites runs many goroutines against a live Start
// loop (run with -race); each (composition, key) pair has exactly one writer goroutine, so
// the final value it wrote is unambiguous.
func TestInputRevisionWriteBufferConcurrentWrites(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewCompositionInputRevisionWriteBuffer(cli)
	go w.Start(ctx)

	const compCount, keysPerComp, iterations = 3, 2, 15

	nsns := make([]client.ObjectKey, compCount)
	for i := range nsns {
		comp := newTestComposition(t, cli, fmt.Sprintf("test-comp-%d", i))
		nsns[i] = client.ObjectKeyFromObject(comp)
	}

	var mu sync.Mutex
	want := make(map[client.ObjectKey]map[string]string, compCount) // "" means removed
	for _, nsn := range nsns {
		want[nsn] = make(map[string]string, keysPerComp)
	}

	var wg sync.WaitGroup
	for c := 0; c < compCount; c++ {
		for k := 0; k < keysPerComp; k++ {
			nsn, key := nsns[c], fmt.Sprintf("key-%d", k)
			wg.Add(1)
			go func() {
				defer wg.Done()
				var last string
				for i := 0; i < iterations; i++ {
					if rand.IntN(4) == 0 {
						w.RemoveInputRevisionAsync(nsn, key)
						last = ""
					} else {
						last = strconv.Itoa(i)
						w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: key, ResourceVersion: last})
					}
				}
				mu.Lock()
				want[nsn][key] = last
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	testutil.Eventually(t, func() bool {
		w.mut.Lock()
		defer w.mut.Unlock()
		return len(w.state) == 0 && w.queue.Len() == 0
	})

	for _, nsn := range nsns {
		comp := &apiv1.Composition{}
		require.NoError(t, cli.Get(ctx, nsn, comp))
		for key, rv := range want[nsn] {
			assert.Equal(t, rv, revisionFor(comp, key), "key %s on %s", key, nsn.Name)
		}
	}
}

func TestInputRevisionWriteBufferUsesConfiguredWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(testutil.NewContext(t))
	release := make(chan struct{})
	var active, maxActive atomic.Int32
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			current := active.Add(1)
			defer active.Add(-1)
			for current > maxActive.Load() && !maxActive.CompareAndSwap(maxActive.Load(), current) {
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
			return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
		},
	})
	w := newCompositionInputRevisionWriteBuffer(cli, 4)

	for i := 0; i < 4; i++ {
		comp := newTestComposition(t, cli, fmt.Sprintf("parallel-comp-%d", i))
		w.PatchInputRevisionAsync(client.ObjectKeyFromObject(comp), &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	}

	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	testutil.Eventually(t, func() bool { return maxActive.Load() == 4 })
	close(release)
	testutil.Eventually(t, func() bool {
		w.mut.Lock()
		defer w.mut.Unlock()
		return len(w.state) == 0 && w.queue.Len() == 0
	})
	cancel()
	require.NoError(t, <-done)
}

// TestInputRevisionWriteBufferIntegrationStorageEdgeCases runs against a real apiserver
// (envtest), since a fresh composition with no status key yet, and an emptied list, are
// storage-layer behaviors the fake client can't reproduce.
func TestInputRevisionWriteBufferIntegrationStorageEdgeCases(t *testing.T) {
	ctx := testutil.NewContext(t)
	mgr := testutil.NewManager(t)
	cli := mgr.GetClient()
	w, err := NewCompositionInputRevisionWriteBufferForManager(mgr.Manager)
	require.NoError(t, err)
	mgr.Start(t)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Spec.Synthesizer.Name = "test-synth"
	require.NoError(t, cli.Create(ctx, comp))
	nsn := client.ObjectKeyFromObject(comp)

	// (a) First-ever status write on a fresh composition: the apiserver has never
	// initialized status for this object, so it may be entirely absent server-side.
	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "foo", ResourceVersion: "1"})
	testutil.Eventually(t, func() bool {
		if err := cli.Get(ctx, nsn, comp); err != nil {
			return false
		}
		return len(comp.Status.InputRevisions) == 1 && comp.Status.InputRevisions[0].Key == "foo"
	})

	// (b) Remove the only input revision, then add a different one: the field must not
	// get stuck as a stored [] that a later flush can never write past.
	w.RemoveInputRevisionAsync(nsn, "foo")
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, nsn, comp)
		return len(comp.Status.InputRevisions) == 0
	})

	w.PatchInputRevisionAsync(nsn, &apiv1.InputRevisions{Key: "bar", ResourceVersion: "1"})
	testutil.Eventually(t, func() bool {
		cli.Get(ctx, nsn, comp)
		return len(comp.Status.InputRevisions) == 1 && comp.Status.InputRevisions[0].Key == "bar"
	})
}
