package flowcontrol

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/internal/testutil"
)

func TestResourceSliceStatusUpdateBasics(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewResourceSliceWriteBuffer(cli)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, setReconciled())

	// Slice resource's status should reflect the patch
	w.processQueueItem(ctx)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 3)
	assert.False(t, slice.Status.Resources[0].Reconciled)
	assert.True(t, slice.Status.Resources[1].Reconciled)
	assert.False(t, slice.Status.Resources[2].Reconciled)

	// All state has been flushed
	assert.Len(t, w.state, 0)
	assert.Equal(t, 0, w.queue.Len())
}

func TestResourceSliceStatusUpdateOrdering(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewResourceSliceWriteBuffer(cli)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled twice
	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, setReconciled())

	req = &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, func(rs *apiv1.ResourceState) *apiv1.ResourceState {
		if rs != nil && !rs.Reconciled {
			return nil
		}
		return &apiv1.ResourceState{Reconciled: false}
	})

	// Slice resource's status should reflect the patch
	w.processQueueItem(ctx)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 3)
	assert.False(t, slice.Status.Resources[0].Reconciled)
	assert.False(t, slice.Status.Resources[1].Reconciled)
	assert.False(t, slice.Status.Resources[2].Reconciled)

	// All state has been flushed
	assert.Len(t, w.state, 0)
	assert.Equal(t, 0, w.queue.Len())
}

func TestResourceSliceStatusUpdateBatching(t *testing.T) {
	ctx := testutil.NewContext(t)
	var patchCalls atomic.Int32
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			patchCalls.Add(1)
			return client.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
		},
	})
	w := NewResourceSliceWriteBuffer(cli)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// Two of the resources have been reconciled within the batch interval
	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, setReconciled())

	req = &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 2
	w.PatchStatusAsync(ctx, req, setReconciled())

	// Slice resource's status should be correct after a single update
	w.processQueueItem(ctx)
	assert.Equal(t, int32(1), patchCalls.Load())
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 3)
	assert.False(t, slice.Status.Resources[0].Reconciled)
	assert.True(t, slice.Status.Resources[1].Reconciled)
	assert.True(t, slice.Status.Resources[2].Reconciled)
}

func TestResourceSliceStatusUpdateNoUpdates(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewResourceSliceWriteBuffer(cli)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, setReconciled())

	// Remove the update leaving the queue message in place
	w.state = map[types.NamespacedName][]*resourceSliceStatusUpdate{}

	// Slice's status should not have been initialized
	w.processQueueItem(ctx)
	require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
	require.Len(t, slice.Status.Resources, 0)
}

func TestResourceSliceStatusUpdateMissingSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClient(t)
	w := NewResourceSliceWriteBuffer(cli)

	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1" // this doesn't exist
	w.PatchStatusAsync(ctx, req, setReconciled())

	// Slice 404 drops the event and does not retry.
	// Prevents a deadlock of this queue item.
	w.processQueueItem(ctx)
	w.processQueueItem(ctx)
	assert.Equal(t, 0, w.queue.Len())
}

// TestResourceSliceStatusUpdateDeletingSlice is identical to TestResourceSliceStatusUpdateDeletingSlice
// except the resource slice still exists in the informer because it or its namespace is being deleted.
func TestResourceSliceStatusUpdateDeletingSlice(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		return k8serrors.NewNotFound(schema.GroupResource{}, "anything")
	}})
	w := NewResourceSliceWriteBuffer(cli)

	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	slice.Status.Resources = make([]apiv1.ResourceState, 3)
	slice.Status.Resources[1].Reconciled = true // already accounted for
	require.NoError(t, cli.Create(ctx, slice))

	req := &resource.ManifestRef{}
	req.Slice.Name = slice.Name
	w.PatchStatusAsync(ctx, req, setReconciled())

	// Status update 404s, drops the event, and does not retry
	w.processQueueItem(ctx)
	w.processQueueItem(ctx)
	assert.Equal(t, 0, w.queue.Len())
}

func TestResourceSliceStatusUpdateNoChange(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			t.Fatal("should not have sent any status updates")
			return nil
		},
	})
	w := NewResourceSliceWriteBuffer(cli)

	// One resource slice
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	slice.Status.Resources = make([]apiv1.ResourceState, 3)
	slice.Status.Resources[1].Reconciled = true // already accounted for
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, setReconciled())

	w.processQueueItem(ctx)
}

func TestResourceSliceStatusUpdateUpdateError(t *testing.T) {
	ctx := testutil.NewContext(t)
	cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			return errors.New("could be any error")
		},
	})
	w := NewResourceSliceWriteBuffer(cli)

	// One resource slice w/ len of 3
	slice := &apiv1.ResourceSlice{}
	slice.Name = "test-slice-1"
	slice.Spec.Resources = make([]apiv1.Manifest, 3)
	require.NoError(t, cli.Create(ctx, slice))

	// One of the resources has been reconciled
	req := &resource.ManifestRef{}
	req.Slice.Name = "test-slice-1"
	req.Index = 1
	w.PatchStatusAsync(ctx, req, setReconciled())

	// Both the queue item and state have persisted
	w.processQueueItem(ctx)
	key := types.NamespacedName{Name: slice.Name}
	assert.Len(t, w.state[key], 1)
	assert.Equal(t, 1, w.queue.NumRequeues(key))
}

func FuzzWriteBuffer(f *testing.F) {
	f.Add(int8(3), int8(1), true, false)
	f.Add(int8(5), int8(2), false, false)
	f.Add(int8(5), int8(2), false, false)
	f.Add(int8(5), int8(2), true, true)

	f.Fuzz(func(t *testing.T, numResources, updateIndex int8, reconciled, clientError bool) {
		ctx := testutil.NewContext(t)
		hasErrored := atomic.Bool{}
		cli := testutil.NewClientWithInterceptors(t, &interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if clientError && !hasErrored.Swap(true) {
					return errors.New("could be any error")
				}
				return nil
			},
		})
		w := NewResourceSliceWriteBuffer(cli)

		slice := &apiv1.ResourceSlice{}
		slice.Name = "test-slice"
		slice.Spec.Resources = make([]apiv1.Manifest, max(numResources, 0))
		require.NoError(t, cli.Create(ctx, slice))

		req := &resource.ManifestRef{}
		req.Slice.Name = slice.Name
		req.Index = int(updateIndex)
		w.PatchStatusAsync(ctx, req, setReconciled())

		for i := 0; i < 5; i++ {
			w.processQueueItem(ctx)
			if len(w.state) == 0 && w.queue.Len() == 0 {
				return
			}
		}
		t.Error("failed to converge")

		require.NoError(t, cli.Get(ctx, client.ObjectKeyFromObject(slice), slice))
		require.Len(t, slice.Status.Resources, int(max(numResources, 0)))

		for i, rs := range slice.Status.Resources {
			if i == int(updateIndex) {
				assert.True(t, rs.Reconciled)
			} else {
				assert.False(t, rs.Reconciled)
			}
		}
	})
}

func setReconciled() StatusPatchFn {
	return func(rs *apiv1.ResourceState) *apiv1.ResourceState {
		if rs != nil && rs.Reconciled {
			return nil
		}
		return &apiv1.ResourceState{Reconciled: true}
	}
}
