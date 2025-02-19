package resource

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

func TestCacheBasics(t *testing.T) {
	ctx := context.Background()
	queue := workqueue.NewTypedRateLimitingQueue[Request](workqueue.DefaultTypedControllerRateLimiter[Request]())
	c := NewCache(nil, queue)

	// Fill doesn't panic when given nil slices
	c.Fill(ctx, types.NamespacedName{}, "", nil)

	// Visit doesn't panic when given nil slices
	assert.False(t, c.Visit(ctx, &apiv1.Composition{}, "foo", nil))

	// Purge doesn't panic when given nil comp and empty nsn
	c.Purge(ctx, types.NamespacedName{}, nil)

	// Get returns false when the synthesis doesn't exist
	_, visible, found := c.Get(ctx, "foo", Ref{})
	assert.False(t, visible)
	assert.False(t, found)

	// Load a synthesis into the cache
	comp := &apiv1.Composition{}
	comp.Name = "foo"
	comp.Namespace = "bar"
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	const synUUID = "foobar"

	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{{
				Manifest: `{ "apiVersion": "v1", "kind": "Pod", "metadata": { "name": "foo", "namespace": "default" } }`,
			}},
		},
	}, {
		ObjectMeta: metav1.ObjectMeta{Name: "slice-2"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{{
				Manifest: `{ "apiVersion": "v1", "kind": "Pod", "metadata": { "name": "bar", "namespace": "default" } }`,
			}},
		},
	}}

	// Nothing is enqueued when visiting a synthesis that isn't in the cache yet
	assert.False(t, c.Visit(ctx, comp, synUUID, slices))
	assert.False(t, c.Visit(ctx, comp, synUUID, slices))
	assert.ElementsMatch(t, []string{}, dumpQueue(queue))

	// Filling the cache does not enqueue anything
	c.Fill(ctx, compNSN, synUUID, slices)
	assert.ElementsMatch(t, []string{}, dumpQueue(queue))

	// Each visible resource is enqueued when visiting the state for the first time
	assert.True(t, c.Visit(ctx, comp, synUUID, slices))
	assert.ElementsMatch(t, []string{"(.Pod)/default/foo", "(.Pod)/default/bar"}, dumpQueue(queue))

	// Visiting again doesn't enqueue anything
	assert.True(t, c.Visit(ctx, comp, synUUID, slices))
	assert.ElementsMatch(t, []string{}, dumpQueue(queue))

	// Get works
	res, visible, found := c.Get(ctx, synUUID, Ref{Name: "foo", Namespace: "default", Kind: "Pod"})
	assert.NotNil(t, res)
	assert.True(t, visible)
	assert.True(t, found)

	// Get doesn't panic when getting a resource that doesn't exist from the otherwise valid synthesis
	res, visible, found = c.Get(ctx, synUUID, Ref{Name: "not-a-pod", Namespace: "default", Kind: "Pod"})
	assert.Nil(t, res)
	assert.False(t, visible)
	assert.False(t, found)

	// Purge basics
	c.Purge(ctx, compNSN, nil)
	assert.False(t, c.Visit(ctx, comp, synUUID, slices))
}

func TestCachePurge(t *testing.T) {
	for i := 0; i < 5; i++ {
		ctx := context.Background()
		queue := workqueue.NewTypedRateLimitingQueue[Request](workqueue.DefaultTypedControllerRateLimiter[Request]())
		c := NewCache(nil, queue)

		comp := &apiv1.Composition{}
		comp.Name = "foo"
		comp.Namespace = "bar"
		compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

		slices := []apiv1.ResourceSlice{{
			Spec: apiv1.ResourceSliceSpec{
				Resources: []apiv1.Manifest{{
					Manifest: `{ "apiVersion": "v1", "kind": "Pod", "metadata": { "name": "foo", "namespace": "default" } }`,
				}},
			},
		}}

		// Fill two syntheses
		c.Fill(ctx, compNSN, "syn-a", slices)
		c.Visit(ctx, comp, "syn-a", slices)
		c.Fill(ctx, compNSN, "syn-b", slices)
		c.Visit(ctx, comp, "syn-b", slices)
		dumpQueue(queue)

		switch i {
		case 0: // purge current only
			c.Purge(ctx, compNSN, &apiv1.Composition{Status: apiv1.CompositionStatus{CurrentSynthesis: &apiv1.Synthesis{UUID: "syn-a"}}})
			assert.True(t, c.Visit(ctx, comp, "syn-a", slices))
			assert.False(t, c.Visit(ctx, comp, "syn-b", slices))

		case 1: // purge previous only
			c.Purge(ctx, compNSN, &apiv1.Composition{Status: apiv1.CompositionStatus{PreviousSynthesis: &apiv1.Synthesis{UUID: "syn-a"}}})
			assert.True(t, c.Visit(ctx, comp, "syn-a", slices))
			assert.False(t, c.Visit(ctx, comp, "syn-b", slices))

		case 2: // purge none
			c.Purge(ctx, compNSN, &apiv1.Composition{Status: apiv1.CompositionStatus{
				CurrentSynthesis:  &apiv1.Synthesis{UUID: "syn-a"},
				PreviousSynthesis: &apiv1.Synthesis{UUID: "syn-b"},
			}})
			assert.True(t, c.Visit(ctx, comp, "syn-a", slices))
			assert.True(t, c.Visit(ctx, comp, "syn-b", slices))

		case 3: // purge both non-nil comp
			c.Purge(ctx, compNSN, &apiv1.Composition{})
			assert.False(t, c.Visit(ctx, comp, "syn-a", slices))
			assert.False(t, c.Visit(ctx, comp, "syn-b", slices))

		case 4: // purge both nil comp
			c.Purge(ctx, compNSN, nil)
			assert.False(t, c.Visit(ctx, comp, "syn-a", slices))
			assert.False(t, c.Visit(ctx, comp, "syn-b", slices))
		}
	}
}

func TestCacheReadinessGroups(t *testing.T) {
	ctx := context.Background()
	queue := workqueue.NewTypedRateLimitingQueue[Request](workqueue.DefaultTypedControllerRateLimiter[Request]())
	c := NewCache(nil, queue)

	comp := &apiv1.Composition{}
	comp.Name = "foo"
	comp.Namespace = "bar"
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

	slices := []apiv1.ResourceSlice{{
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{ "apiVersion": "v1", "kind": "Pod", "metadata": { "name": "foo", "namespace": "default", "annotations": { "eno.azure.io/readiness-group": "-1" } } }`},
				{Manifest: `{ "apiVersion": "v1", "kind": "Pod", "metadata": { "name": "bar", "namespace": "default", "annotations": { "eno.azure.io/readiness-group": "3" } } }`},
				{Manifest: `{ "apiVersion": "v1", "kind": "Pod", "metadata": { "name": "baz", "namespace": "default", "annotations": { "eno.azure.io/readiness-group": "9001" } } }`},
			},
		},
	}}

	const synUUID = "foobar"
	c.Fill(ctx, compNSN, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)
	dumpQueue(queue)

	podIsVisible := func(name string, exp bool) {
		_, visible, found := c.Get(ctx, synUUID, Ref{Name: name, Namespace: "default", Kind: "Pod"})
		assert.Equal(t, exp, visible, name)
		assert.True(t, found, name)
	}
	podIsVisible("foo", true)
	podIsVisible("bar", false)
	podIsVisible("baz", false)

	slices[0].Status.Resources = []apiv1.ResourceState{{Ready: &metav1.Time{}}, {}, {}}
	c.Visit(ctx, comp, synUUID, slices)
	assert.ElementsMatch(t, []string{"(.Pod)/default/foo", "(.Pod)/default/bar"}, dumpQueue(queue))
	podIsVisible("foo", true)
	podIsVisible("bar", true)
	podIsVisible("baz", false)

	slices[0].Status.Resources = []apiv1.ResourceState{{Ready: &metav1.Time{}}, {Ready: &metav1.Time{}}, {}}
	c.Visit(ctx, comp, synUUID, slices)
	assert.ElementsMatch(t, []string{"(.Pod)/default/bar", "(.Pod)/default/baz"}, dumpQueue(queue))
	podIsVisible("foo", true)
	podIsVisible("bar", true)
	podIsVisible("baz", true)
}

func dumpQueue(q workqueue.TypedRateLimitingInterface[Request]) (slice []string) {
	for {
		if q.Len() == 0 {
			return
		}
		req, _ := q.Get()
		slice = append(slice, req.Resource.String())
		q.Done(req)
		q.Forget(req)
	}
}
