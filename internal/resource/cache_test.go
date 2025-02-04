package resource

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
)

// TODO: TEST THE VISIT FUNC

func TestCacheBasics(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[*Request]())
	c := NewCache(nil, queue)

	const synUUID = "test-synthesis"
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	slices := []apiv1.ResourceSlice{
		{Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo1" } }`},
			},
		}},
	}

	// Visit/fill
	assert.False(t, c.Visit(ctx, comp, synUUID, slices), "visit before filling cache")
	c.Fill(ctx, comp, synUUID, slices)
	assert.True(t, c.Visit(ctx, comp, synUUID, slices), "visit after filling cache")
	assert.Equal(t, 1, queue.Len())

	// Get
	syn, found := c.Get(synUUID)
	require.True(t, found)

	res, found := syn.Get(&Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"})
	require.True(t, found)
	assert.Equal(t, "foo.bar.io/v1, Kind=Foo", res.GVK.String())

	res, found = syn.GetByIndex(slices[0].Name, 0)
	require.True(t, found)
	assert.Equal(t, "foo.bar.io/v1, Kind=Foo", res.GVK.String())

	// Purge
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	c.Purge(ctx, compNSN, nil)
	assert.False(t, c.Visit(ctx, comp, synUUID, slices), "visit after purging cache")

	// Get (not found)
	_, found = c.Get(synUUID)
	require.False(t, found)
}

func TestCacheReadinessGroups(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[*Request]())
	c := NewCache(nil, queue)

	const synUUID = "test-synthesis"
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	slices := []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo1", "annotations": { "eno.azure.io/readiness-group": "2" } } }`},  // ready (skip group 1)
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo3", "annotations": { "eno.azure.io/readiness-group": "-2" } } }`}, // negative
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo2", "annotations": { "eno.azure.io/readiness-group": "3" } } }`},  // non-ready (adjacent to group 2)
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo5", "annotations": { "eno.azure.io/readiness-group": "3" } } }`},  // ready (adjacent to group 2)
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo4" } }`},                                                          // no group (default to 0)
			},
		},
		Status: apiv1.ResourceSliceStatus{
			Resources: []apiv1.ResourceState{
				{Ready: ptr.To(metav1.Now())},
				{},
				{},
				{Ready: ptr.To(metav1.Now())},
				{},
			},
		},
	}}

	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)

	syn, _ := c.Get(synUUID)
	assert.True(t, syn.ReadinessGroupIsReady(-2))
	assert.True(t, syn.ReadinessGroupIsReady(-1))
	assert.True(t, syn.ReadinessGroupIsReady(0))
	assert.True(t, syn.ReadinessGroupIsReady(1))
	assert.True(t, syn.ReadinessGroupIsReady(2))
	assert.False(t, syn.ReadinessGroupIsReady(4))
}

func TestPurge(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[*Request]())
	c := NewCache(nil, queue)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	comp.Status.CurrentSynthesis = &apiv1.Synthesis{
		UUID:           "test-synthesis",
		Synthesized:    ptr.To(metav1.Now()),
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "slice1"}},
	}
	comp.Status.PreviousSynthesis = &apiv1.Synthesis{
		UUID:           "test-previous-synthesis",
		Synthesized:    ptr.To(metav1.Now()),
		ResourceSlices: []*apiv1.ResourceSliceRef{{Name: "slice2"}},
	}
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

	slices := []apiv1.ResourceSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice1"},
			Spec: apiv1.ResourceSliceSpec{
				Resources: []apiv1.Manifest{
					{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo1" } }`},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "slice2"},
			Spec: apiv1.ResourceSliceSpec{
				Resources: []apiv1.Manifest{
					{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo2" } }`},
				},
			},
		},
	}

	c.Fill(ctx, comp, comp.Status.PreviousSynthesis.UUID, slices)
	c.Fill(ctx, comp, comp.Status.CurrentSynthesis.UUID, slices)
	c.Purge(ctx, compNSN, comp)

	// Confirm that both syntheses exist in the cache
	_, ok := c.Get(comp.Status.CurrentSynthesis.UUID)
	require.True(t, ok)

	_, ok = c.Get(comp.Status.PreviousSynthesis.UUID)
	require.True(t, ok)

	// Purge the previous synthesis
	prevUUID := comp.Status.PreviousSynthesis.UUID
	comp.Status.PreviousSynthesis = nil
	c.Purge(ctx, compNSN, comp)

	_, ok = c.Get(comp.Status.CurrentSynthesis.UUID)
	require.True(t, ok)

	_, ok = c.Get(prevUUID)
	require.False(t, ok)

	// Purge the current synthesis
	currentUUID := comp.Status.CurrentSynthesis.UUID
	comp.Status.CurrentSynthesis = nil
	c.Purge(ctx, compNSN, comp)

	_, ok = c.Get(currentUUID)
	require.False(t, ok)

	_, ok = c.Get(prevUUID)
	require.False(t, ok)
}

func newContext(t *testing.T) context.Context { // copied from testutil to avoid circular dependency
	return logr.NewContext(context.Background(), testr.NewWithOptions(t, testr.Options{Verbosity: 2}))
}
