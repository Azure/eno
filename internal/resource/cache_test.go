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

// TestCacheGhostResourceCrossReconcilerDeletionOrder simulates two eno-reconcilers (A and B)
// sharing the same composition. Reconciler A manages a CRD-like resource (deletion-group -1)
// and reconciler B manages a Deployment-like resource (deletion-group 0).
// The test proves that B's resource is blocked until A's resource is confirmed deleted,
// even though A's resource is a ghost (filtered out) in B's cache.
func TestCacheGhostResourceCrossReconcilerDeletionOrder(t *testing.T) {
	ctx := context.Background()

	// Shared composition with DeletionTimestamp set to activate deletion-group logic
	now := metav1.Now()
	comp := &apiv1.Composition{}
	comp.Name = "test-comp"
	comp.Namespace = "test-ns"
	comp.DeletionTimestamp = &now

	// Two resources in the same slice:
	// - "crd-resource" with deletion-group -1, label role=crd (managed by reconciler A)
	// - "deployment-resource" with deletion-group 0, label role=deployment (managed by reconciler B)
	slices := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1", Namespace: "test-ns"},
		Spec: apiv1.ResourceSliceSpec{
			Resources: []apiv1.Manifest{
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "crd-resource", "namespace": "default", "labels": {"role": "crd"}, "annotations": {"eno.azure.io/deletion-group": "-1"}}}`},
				{Manifest: `{"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "deployment-resource", "namespace": "default", "labels": {"role": "deployment"}, "annotations": {"eno.azure.io/deletion-group": "0"}}}`},
			},
		},
	}}

	const synUUID = "test-syn"
	crdRef := Ref{Name: "crd-resource", Namespace: "default", Kind: "ConfigMap"}
	deployRef := Ref{Name: "deployment-resource", Namespace: "default", Kind: "ConfigMap"}

	// --- Reconciler A: manages role=crd resources ---
	filterA, err := enocel.Parse("self.metadata.labels.role == 'crd'")
	require.NoError(t, err)
	var cacheA Cache
	queueA := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	cacheA.SetQueue(queueA)
	cacheA.ResourceFilter = filterA

	cacheA.Fill(ctx, comp, synUUID, slices)
	cacheA.Visit(ctx, comp, synUUID, slices)
	dumpQueue(queueA) // drain

	// Reconciler A can see the crd-resource (it's its own)
	res, visible, found := cacheA.Get(ctx, synUUID, crdRef)
	assert.NotNil(t, res, "reconciler A should find its own resource")
	assert.True(t, found)
	assert.True(t, visible, "crd-resource (deletion-group -1) has no dependencies, should be visible")

	// Reconciler A cannot see deployment-resource (it's a ghost)
	res, visible, found = cacheA.Get(ctx, synUUID, deployRef)
	assert.Nil(t, res)
	assert.False(t, found, "deployment-resource is a ghost in reconciler A's cache")
	assert.False(t, visible)

	// --- Reconciler B: manages role=deployment resources ---
	filterB, err := enocel.Parse("self.metadata.labels.role == 'deployment'")
	require.NoError(t, err)
	var cacheB Cache
	queueB := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[Request]())
	cacheB.SetQueue(queueB)
	cacheB.ResourceFilter = filterB

	cacheB.Fill(ctx, comp, synUUID, slices)
	cacheB.Visit(ctx, comp, synUUID, slices)
	dumpQueue(queueB) // drain

	// Reconciler B cannot see crd-resource (it's a ghost)
	res, visible, found = cacheB.Get(ctx, synUUID, crdRef)
	assert.Nil(t, res)
	assert.False(t, found, "crd-resource is a ghost in reconciler B's cache")
	assert.False(t, visible)

	// Reconciler B can find deployment-resource but it should NOT be visible yet
	// because it depends on crd-resource (deletion-group -1 < 0) which hasn't been deleted
	res, visible, found = cacheB.Get(ctx, synUUID, deployRef)
	assert.NotNil(t, res, "reconciler B should find its own resource")
	assert.True(t, found)
	assert.False(t, visible, "deployment-resource should be blocked by ghost crd-resource in deletion-group -1")

	// --- Simulate reconciler A deleting the crd-resource ---
	// Reconciler A writes Deleted: true to the resource slice status for the crd-resource (index 0)
	slicesWithStatus := []apiv1.ResourceSlice{{
		ObjectMeta: metav1.ObjectMeta{Name: "slice-1", Namespace: "test-ns"},
		Spec:       slices[0].Spec,
		Status: apiv1.ResourceSliceStatus{
			Resources: []apiv1.ResourceState{
				{Deleted: true}, // crd-resource at index 0 is deleted
				{},              // deployment-resource at index 1 is not yet deleted
			},
		},
	}}

	// Reconciler B's informer picks up the status change and calls Visit
	cacheB.Visit(ctx, comp, synUUID, slicesWithStatus)
	enqueuedB := dumpQueue(queueB)

	// The deployment-resource should have been enqueued because its dependency was cleared
	assert.Contains(t, enqueuedB, deployRef.String(), "deployment-resource should be enqueued after ghost dependency is cleared")

	// Now deployment-resource should be visible — the ghost crd-resource dependency is satisfied
	res, visible, found = cacheB.Get(ctx, synUUID, deployRef)
	assert.NotNil(t, res)
	assert.True(t, found)
	assert.True(t, visible, "deployment-resource should now be visible after crd-resource ghost is deleted")
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
