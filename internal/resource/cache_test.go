package resource

import (
	"context"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	enocel "github.com/Azure/eno/internal/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

func TestCacheBasics(t *testing.T) {
	ctx := context.Background()
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	// Fill doesn't panic when given nil slices
	c.Fill(ctx, &apiv1.Composition{}, "", nil)

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
	c.Fill(ctx, comp, synUUID, slices)
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
		var c Cache
		queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
		c.SetQueue(queue)

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
		c.Fill(ctx, comp, "syn-a", slices)
		c.Visit(ctx, comp, "syn-a", slices)
		c.Fill(ctx, comp, "syn-b", slices)
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
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	comp := &apiv1.Composition{}
	comp.Name = "foo"
	comp.Namespace = "bar"

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
	c.Fill(ctx, comp, synUUID, slices)
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

func TestCacheResourceFilter(t *testing.T) {
	ctx := context.Background()
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "test-ns"

	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "allowed", "namespace": "default", "labels": {"env": "prod"}}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "filtered", "namespace": "default", "labels": {"env": "dev"}}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "pod-prod", "namespace": "default", "labels": {"env": "prod"}}}`},
			},
		},
	}}

	filter, err := enocel.Parse("self.metadata.labels.env == 'prod'")
	require.NoError(t, err)
	c.ResourceFilter = filter

	const synUUID = "test-syn"
	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)

	requests := dumpQueue(queue)
	assert.ElementsMatch(t, []string{"(.ConfigMap)/default/allowed", "(.Pod)/default/pod-prod"}, requests)

	configMapRes, visible, found := c.Get(ctx, synUUID, Ref{Name: "allowed", Namespace: "default", Kind: "ConfigMap"})
	assert.NotNil(t, configMapRes)
	assert.True(t, visible)
	assert.True(t, found)

	filteredRes, visible, found := c.Get(ctx, synUUID, Ref{Name: "filtered", Namespace: "default", Kind: "ConfigMap"})
	assert.Nil(t, filteredRes)
	assert.False(t, visible)
	assert.False(t, found)

	podRes, visible, found := c.Get(ctx, synUUID, Ref{Name: "pod-prod", Namespace: "default", Kind: "Pod"})
	assert.NotNil(t, podRes)
	assert.True(t, visible)
	assert.True(t, found)
}

func TestCacheResourceFilterWithCompositionContext(t *testing.T) {
	ctx := context.Background()
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	comp := &apiv1.Composition{}
	comp.Name = "my-special-comp"
	comp.Namespace = "prod-ns"
	comp.Labels = map[string]string{"team": "platform"}

	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "resource-1", "namespace": "default"}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "resource-2", "namespace": "default"}}`},
			},
		},
	}}

	filter, err := enocel.Parse("composition.metadata.name == 'my-special-comp' && composition.metadata.labels.team == 'platform' && self.kind == 'ConfigMap'")
	require.NoError(t, err)
	c.ResourceFilter = filter

	const synUUID = "test-syn"
	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)

	requests := dumpQueue(queue)
	assert.ElementsMatch(t, []string{"(.ConfigMap)/default/resource-1"}, requests)

	configMapRes, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-1", Namespace: "default", Kind: "ConfigMap"})
	assert.NotNil(t, configMapRes)
	assert.True(t, visible)
	assert.True(t, found)

	secretRes, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-2", Namespace: "default", Kind: "Secret"})
	assert.Nil(t, secretRes)
	assert.False(t, visible)
	assert.False(t, found)
}

func TestCacheResourceFilterNilFilter(t *testing.T) {
	ctx := context.Background()
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "test-ns"

	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "resource-1", "namespace": "default", "labels": {"env": "prod"}}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "resource-2", "namespace": "default", "labels": {"env": "dev"}}}`},
			},
		},
	}}

	c.ResourceFilter = nil

	const synUUID = "test-syn"
	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)

	requests := dumpQueue(queue)
	assert.ElementsMatch(t, []string{"(.ConfigMap)/default/resource-1", "(.ConfigMap)/default/resource-2"}, requests)

	res1, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-1", Namespace: "default", Kind: "ConfigMap"})
	assert.NotNil(t, res1)
	assert.True(t, visible)
	assert.True(t, found)

	res2, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-2", Namespace: "default", Kind: "ConfigMap"})
	assert.NotNil(t, res2)
	assert.True(t, visible)
	assert.True(t, found)
}

func TestCacheResourceFilterAlwaysFalse(t *testing.T) {
	ctx := context.Background()
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "test-ns"

	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "resource-1", "namespace": "default"}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "resource-2", "namespace": "default"}}`},
			},
		},
	}}

	filter, err := enocel.Parse("false")
	require.NoError(t, err)
	c.ResourceFilter = filter

	const synUUID = "test-syn"
	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)

	requests := dumpQueue(queue)
	assert.ElementsMatch(t, []string{}, requests)

	res1, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-1", Namespace: "default", Kind: "ConfigMap"})
	assert.Nil(t, res1)
	assert.False(t, visible)
	assert.False(t, found)

	res2, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-2", Namespace: "default", Kind: "Pod"})
	assert.Nil(t, res2)
	assert.False(t, visible)
	assert.False(t, found)
}

func TestCacheResourceFilterAlwaysTrue(t *testing.T) {
	ctx := context.Background()
	var c Cache
	queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	c.SetQueue(queue)

	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "test-ns"

	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "resource-1", "namespace": "default"}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "resource-2", "namespace": "default"}}`},
			},
		},
	}}

	filter, err := enocel.Parse("true")
	require.NoError(t, err)
	c.ResourceFilter = filter

	const synUUID = "test-syn"
	c.Fill(ctx, comp, synUUID, slices)
	c.Visit(ctx, comp, synUUID, slices)

	requests := dumpQueue(queue)
	assert.ElementsMatch(t, []string{"(.ConfigMap)/default/resource-1", "(.Pod)/default/resource-2"}, requests)

	res1, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-1", Namespace: "default", Kind: "ConfigMap"})
	assert.NotNil(t, res1)
	assert.True(t, visible)
	assert.True(t, found)

	res2, visible, found := c.Get(ctx, synUUID, Ref{Name: "resource-2", Namespace: "default", Kind: "Pod"})
	assert.NotNil(t, res2)
	assert.True(t, visible)
	assert.True(t, found)
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
