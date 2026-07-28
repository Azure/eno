package flowcontrol

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
