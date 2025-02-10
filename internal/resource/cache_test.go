package resource

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
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

// TODO: Test behavior with conflicting resource refs

// TODO: Test resource group + crd ordering

func TestCacheBasics(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[Request]())
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

	// Resources are visible when their synthesis isn't known by the cache
	// (to avoid blocking the loop in strange/unexpected cases)
	assert.True(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"}))

	// Visit/fill
	assert.False(t, c.Visit(ctx, comp, synUUID, slices), "visit before filling cache")
	c.Fill(ctx, comp, synUUID, slices)
	assert.True(t, c.Visit(ctx, comp, synUUID, slices), "visit after filling cache")
	assert.Equal(t, 1, queue.Len())

	// Resources are visible when they aren't known by the cache
	assert.True(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "not-in-the-cache"}))

	// Get
	res, found := c.Get(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"})
	require.True(t, found)
	assert.Equal(t, "foo.bar.io/v1, Kind=Foo", res.GVK.String())

	// Visible
	assert.True(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"}))

	// GC
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	c.GC(ctx, compNSN, nil)
	assert.False(t, c.Visit(ctx, comp, synUUID, slices), "visit after purging cache")

	// Get (not found)
	_, found = c.Get(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"})
	require.False(t, found)
}

func TestCacheFuzzReadinessGroups(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[Request]())
	c := NewCache(nil, queue)

	for i := 0; i < 8; i++ {
		const nResources = 500
		const nGroups = 100

		groups := make([]int, nGroups)
		ready := make([]bool, nResources)
		reconciled := make([]bool, nResources)
		resourceToGroup := make([]int, nResources)

		maxGrp := len(groups) * 3
		for i := range groups {
			groups[i] = rand.IntN(maxGrp) - (maxGrp / 2)
		}

		const synUUID = "test-synthesis"
		comp := &apiv1.Composition{}
		comp.Name = "test-comp"
		comp.Namespace = "default"

		resources := []apiv1.ResourceSlice{{}}
		resources[0].Spec.Resources = make([]apiv1.Manifest, nResources)
		resources[0].Status.Resources = make([]apiv1.ResourceState, nResources)
		for j := 0; j < nResources; j++ {
			group := groups[rand.IntN(len(groups))]
			resourceToGroup[j] = group
			resources[0].Spec.Resources[j] = apiv1.Manifest{
				Manifest: fmt.Sprintf(`{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "resource-%d", "annotations": { "eno.azure.io/readiness-group": "%d" } } }`, j, group),
			}
		}
		c.Fill(ctx, comp, synUUID, resources)

		// Randomly pick resources to become reconciled and/or ready
		advanceStatus := func() {
			for j := 0; j < nResources; j++ {
				if !ready[j] {
					ready[j] = rand.IntN(5) == 0
				}
				if !reconciled[j] {
					reconciled[j] = rand.IntN(10) == 0
				}

				state := apiv1.ResourceState{Reconciled: reconciled[j]}
				if ready[j] {
					state.Ready = ptr.To(metav1.Now())
				}
				resources[0].Status.Resources[j] = state
			}
			c.Visit(ctx, comp, synUUID, resources)
		}

		var cursor int
		for j := 0; j < 50; j++ {
			advanceStatus()

			// Find the highest readiness group in which all of the resources are currently ready
			readyGroups := map[int]struct{}{}
			cursor = math.MaxInt
			for k, grp := range resourceToGroup {
				cursor = min(cursor, grp)
				if ready[k] {
					readyGroups[grp] = struct{}{}
				}
			}
			for k, grp := range resourceToGroup {
				if !ready[k] {
					delete(readyGroups, grp)
				}
			}
			for grp := range readyGroups {
				if grp > cursor {
					cursor = grp
				}
			}

			// No resource after the last ready group should be visible
			for k, group := range resourceToGroup {
				assert.Equal(t,
					group <= cursor,
					c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: fmt.Sprintf("resource-%d", k)}),
					"resource=%d group=%d maxReadyGroup=%d", k, group, cursor)
			}
		}

		// Each visible and non-reconciled resource should have been enqueued
		workItems := flushQueue(queue)
		for i, group := range groups {
			if group <= cursor && !reconciled[i] {
				assert.Contains(t, workItems, fmt.Sprintf("resource-%d", i))
			}
		}
	}
}

func TestCacheVisitReadinessGroupTransition(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[Request]())
	c := NewCache(nil, queue)

	const synUUID = "test-synthesis"
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	slices := []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo1", "annotations": { "eno.azure.io/readiness-group": "2" } } }`},
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo2", "annotations": { "eno.azure.io/readiness-group": "3" } } }`},
			},
		},
		Status: apiv1.ResourceSliceStatus{
			Resources: []apiv1.ResourceState{
				{},
				{},
			},
		},
	}}

	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)
	assert.ElementsMatch(t, flushQueue(queue), []string{"foo1", "foo2"})

	c.Visit(ctx, comp, synUUID, slices)
	assert.ElementsMatch(t, flushQueue(queue), []string{})

	slices[0].Status.Resources[0].Ready = ptr.To(metav1.Now())
	c.Visit(ctx, comp, synUUID, slices)
	assert.ElementsMatch(t, flushQueue(queue), []string{"foo1"})

	c.Visit(ctx, comp, synUUID, slices)
	assert.ElementsMatch(t, flushQueue(queue), []string{})
}

func TestCacheVisitCRDTransition(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[Request]())
	c := NewCache(nil, queue)

	const synUUID = "test-synthesis"
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	slices := []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "test-cr" } }`},
				{Manifest: `{ "kind": "CustomResourceDefinition", "apiVersion": "apiextensions.k8s.io/v1", "metadata": { "name": "test-crd" }, "spec": { "group": "foo.bar.io", "names": { "kind": "Foo" } } }`},
			},
		},
		Status: apiv1.ResourceSliceStatus{
			Resources: []apiv1.ResourceState{
				{},
				{},
			},
		},
	}}

	c.Fill(ctx, comp, synUUID, slices)
	assert.False(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "test-cr"}))
	assert.ElementsMatch(t, []string{}, flushQueue(queue))

	c.Visit(ctx, comp, synUUID, slices)
	assert.False(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "test-cr"}))
	assert.ElementsMatch(t, []string{"test-cr", "test-crd"}, flushQueue(queue))

	c.Visit(ctx, comp, synUUID, slices)
	assert.False(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "test-cr"}))
	assert.ElementsMatch(t, []string{}, flushQueue(queue))

	slices[0].Status.Resources[1].Reconciled = true
	c.Visit(ctx, comp, synUUID, slices)
	assert.True(t, c.Visible(synUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "test-cr"}))
	assert.ElementsMatch(t, []string{"test-cr", "test-crd"}, flushQueue(queue))
}

func TestCacheVisitStateTransition(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[Request]())
	c := NewCache(nil, queue)

	const synUUID = "test-synthesis"
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "default"
	slices := []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{ "kind": "Foo", "apiVersion": "foo.bar.io/v1", "metadata": { "name": "foo1", "annotations": { "eno.azure.io/readiness-group": "2" } } }`}, // ready (skip group 1)
			},
		},
		Status: apiv1.ResourceSliceStatus{
			Resources: []apiv1.ResourceState{
				{},
			},
		},
	}}

	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)
	assert.Equal(t, flushQueue(queue), []string{"foo1"})

	c.Visit(ctx, comp, synUUID, slices)
	assert.Equal(t, flushQueue(queue), []string{})

	slices[0].Status.Resources[0].Ready = ptr.To(metav1.Now())
	c.Visit(ctx, comp, synUUID, slices)
	assert.Equal(t, flushQueue(queue), []string{"foo1"})

	c.Visit(ctx, comp, synUUID, slices)
	assert.Equal(t, flushQueue(queue), []string{})
}

func TestGC(t *testing.T) {
	ctx := newContext(t)
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedItemBasedRateLimiter[Request]())
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
	c.GC(ctx, compNSN, comp)

	// Confirm that both resources exist in the cache
	_, ok := c.Get(comp.Status.CurrentSynthesis.UUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"})
	require.True(t, ok)

	_, ok = c.Get(comp.Status.PreviousSynthesis.UUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo2"})
	require.True(t, ok)

	// Purge the previous synthesis
	prevUUID := comp.Status.PreviousSynthesis.UUID
	comp.Status.PreviousSynthesis = nil
	c.GC(ctx, compNSN, comp)

	_, ok = c.Get(comp.Status.CurrentSynthesis.UUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"})
	require.True(t, ok)

	_, ok = c.Get(prevUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo2"})
	require.False(t, ok)

	// Purge the current synthesis
	currentUUID := comp.Status.CurrentSynthesis.UUID
	comp.Status.CurrentSynthesis = nil
	c.GC(ctx, compNSN, comp)

	_, ok = c.Get(currentUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo1"})
	require.False(t, ok)

	_, ok = c.Get(prevUUID, &Ref{Group: "foo.bar.io", Kind: "Foo", Name: "foo2"})
	require.False(t, ok)
}

func newContext(t *testing.T) context.Context { // copied from testutil to avoid circular dependency
	return logr.NewContext(context.Background(), testr.NewWithOptions(t, testr.Options{Verbosity: 2}))
}

func flushQueue(queue workqueue.TypedRateLimitingInterface[Request]) (items []string) {
	items = []string{}
	for {
		if queue.Len() == 0 {
			return
		}
		req, _ := queue.Get()
		items = append(items, req.Resource.Name)
		queue.Forget(req)
		queue.Done(req)
	}
}
