package reconstitution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/testutil"
)

func TestWriteBufferBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := newWriteBuffer(cli, 0, 1)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req)

	// Slice resource's status should reflect the patch
	w.processQueueItem(ctx)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 3)
	assert.False(t, slice.Status.Resources[0].Reconciled)
	assert.True(t, slice.Status.Resources[1].Reconciled)
	assert.False(t, slice.Status.Resources[2].Reconciled)

	// All state has been flushed
	assert.Len(t, w.state, 0)
	w.processQueueItem(ctx)
	assert.Equal(t, 0, w.queue.Len())
}

func TestWriteBufferBatching(t *testing.T) {
	ctx := testutil.NewContext(t)
	var updateCalls atomic.Int32
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			updateCalls.Add(1)
			return client.SubResource(subResourceName).Update(ctx, obj, opts...)
		},
	})
	w := newWriteBuffer(cli, time.Millisecond*2, 1)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// Two of the resources have been reconciled within the batch interval
	req := &ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req)

	req = &ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 2
	w.PatchStatusAsync(ctx, req)

	// Slice resource's status should be correct after a single update
	w.processQueueItem(ctx)
	assert.Equal(t, int32(1), updateCalls.Load())
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 3)
	assert.False(t, slice.Status.Resources[0].Reconciled)
	assert.True(t, slice.Status.Resources[1].Reconciled)
	assert.True(t, slice.Status.Resources[2].Reconciled)
}

func TestWriteBufferNoUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := newWriteBuffer(cli, 0, 1)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req)

	// Remove the update leaving the queue message in place
	w.state = map[types.NamespacedName][]int{}

	// Slice's status should not have been initialized
	w.processQueueItem(ctx)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 0)
}

func TestWriteBufferMissingSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := newWriteBuffer(cli, 0, 1)

	req := &ManifestRef{}
	req.Slice.Name = "test-slice-1" // this doesn't exist
	w.PatchStatusAsync(ctx, req)

	// Slice 404 drops the event and does not retry.
	// Prevents a deadlock of this queue item.
	w.processQueueItem(ctx)
	w.processQueueItem(ctx)
	assert.Equal(t, 0, w.queue.Len())
}

func TestWriteBufferNoChange(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			t.Fatal("should not have sent any status updates")
			return nil
		},
	})
	w := newWriteBuffer(cli, 0, 1)

	// One resource slice
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	slice.Status.Resources = make([]apiv1.ResourceState, 3)
	slice.Status.Resources[1].Reconciled = true // already accounted for
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req)

	w.processQueueItem(ctx)
}

func TestWriteBufferUpdateError(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return errors.New("could be any error")
		},
	})
	w := newWriteBuffer(cli, 0, 1)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req)

	// Both the queue item and state have persisted
	w.processQueueItem(ctx)
	key := types.NamespacedName{Name: slice.Name}
	assert.Len(t, w.state[key], 1)
	assert.Equal(t, 1, w.queue.Len())
}
