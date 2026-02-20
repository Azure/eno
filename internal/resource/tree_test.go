package resource

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
)

func TestTreeBuilderSanity(t *testing.T) {
	var tests = []struct {
		Name      string
		Resources []*Resource
	}{
		{
			Name: "empty",
		},
		{
			Name: "single-basic-resource",
			Resources: []*Resource{{
				Ref: newTestRef("test-resource"),
			}},
		},
		{
			Name: "several-readiness-groups",
			Resources: []*Resource{
				{
					Ref:            newTestRef("test-negative-2"),
					readinessGroup: -2,
				},
				{
					Ref:            newTestRef("test-1"),
					readinessGroup: 1,
				},
				{
					Ref: newTestRef("test-0"),
				},
				{
					Ref:            newTestRef("test-4"),
					readinessGroup: 4,
				},
			},
		},
		{
			Name: "several-overlapping-groups",
			Resources: []*Resource{
				{
					Ref:            newTestRef("test-1"),
					readinessGroup: 4,
				},
				{
					Ref:            newTestRef("test-2-a"),
					readinessGroup: 8,
				},
				{
					Ref:            newTestRef("test-2-b"),
					readinessGroup: 8,
				},
			},
		},
		{
			Name: "crd-and-cr",
			Resources: []*Resource{
				{
					Ref: newTestRef("test-cr"),
					GVK: schema.GroupVersionKind{Group: "test.group", Version: "v1", Kind: "TestCRDKind"},
				},
				{
					Ref:              newTestRef("test-crd"),
					DefinedGroupKind: &schema.GroupKind{Group: "test.group", Kind: "TestCRDKind"},
				},
			},
		},
		{
			Name: "both-crd-and-cr-and-readiness-groups",
			Resources: []*Resource{
				{
					Ref:            newTestRef("test-cr"),
					GVK:            schema.GroupVersionKind{Group: "test.group", Version: "v1", Kind: "TestCRDKind"},
					readinessGroup: 5,
				},
				{
					Ref:              newTestRef("test-crd"),
					DefinedGroupKind: &schema.GroupKind{Group: "test.group", Kind: "TestCRDKind"},
					readinessGroup:   3,
				},
				{
					Ref:            newTestRef("also-not-a-crd"),
					readinessGroup: 10,
				},
				{
					Ref:            newTestRef("not-a-crd"),
					readinessGroup: 1,
				},
			},
		},
		{
			Name: "both-crd-and-cr-conflict",
			Resources: []*Resource{
				{
					Ref:            newTestRef("test-cr"),
					GVK:            schema.GroupVersionKind{Group: "test.group", Version: "v1", Kind: "TestCRDKind"},
					readinessGroup: 3,
				},
				{
					Ref:              newTestRef("test-crd"),
					DefinedGroupKind: &schema.GroupKind{Group: "test.group", Kind: "TestCRDKind"},
					readinessGroup:   5,
				},
			},
		},
		{
			Name: "deletion-groups",
			Resources: []*Resource{
				{
					Ref:                newTestRef("no-deletion-or-readiness-group"),
					compositionDeleted: true,
				},
				{
					Ref:                newTestRef("no-deletion-group"),
					readinessGroup:     3,
					compositionDeleted: true,
				},
				{
					Ref:                newTestRef("high-deletion-group"),
					deletionGroup:      ptr.To(9),
					compositionDeleted: true,
				},
				{
					Ref:                newTestRef("low-deletion-group"),
					deletionGroup:      ptr.To(3),
					compositionDeleted: true,
				},
			},
		},
		{
			//should show during deletion we don't take circular dependencies.
			Name: "crd-and-cr-during-deletion",
			Resources: []*Resource{
				{
					Ref:                newTestRef("test-cr"),
					GVK:                schema.GroupVersionKind{Group: "test.group", Version: "v1", Kind: "TestCRDKind"},
					deletionGroup:      ptr.To(1),
					compositionDeleted: true,
				},
				{
					Ref:                newTestRef("test-crd"),
					DefinedGroupKind:   &schema.GroupKind{Group: "test.group", Kind: "TestCRDKind"},
					deletionGroup:      ptr.To(2),
					compositionDeleted: true,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			var b treeBuilder
			for _, r := range tc.Resources {
				b.Add(r)
			}

			tree := b.Build()
			js, err := json.MarshalIndent(tree, "", "  ")
			require.NoError(t, err)

			fixture := fmt.Sprintf("fixtures/tree-builder-%s.json", tc.Name)
			if os.Getenv("UPDATE_SNAPSHOTS") != "" {
				require.NoError(t, os.WriteFile(fixture, js, 0644))
			} else {
				expJS, err := os.ReadFile(fixture)
				require.NoError(t, err)
				assert.JSONEq(t, string(expJS), string(js))
			}
		})
	}
}

func newTestRef(name string) Ref {
	return Ref{
		Group:     "test.group",
		Kind:      "TestKind",
		Namespace: "default",
		Name:      name,
	}
}

func TestTreeVisibility(t *testing.T) {
	var b treeBuilder
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-4"),
		readinessGroup: 4,
		ManifestRef:    ManifestRef{Index: 4},
	})
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-1"),
		readinessGroup: 1,
		ManifestRef:    ManifestRef{Index: 1},
	})
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-3"),
		readinessGroup: 3,
		ManifestRef:    ManifestRef{Index: 3},
	})
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-2"),
		readinessGroup: 2,
		ManifestRef:    ManifestRef{Index: 2},
	})
	names := []string{"test-resource-1", "test-resource-2", "test-resource-3", "test-resource-4"}
	tree := b.Build()

	res, visible, found := tree.Get(newTestRef("foobar"))
	assert.False(t, found, "404 case")
	assert.False(t, visible)
	assert.Nil(t, res)

	tree.UpdateState(ManifestRef{Index: 100}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {}) // it doesn't panic

	// Default readiness
	expectedVisibility := map[string]bool{"test-resource-1": true}
	assertReadiness := func() {
		for _, name := range names {
			res, visible, found := tree.Get(newTestRef(name))
			assert.True(t, found, name)
			assert.Equal(t, expectedVisibility[name], visible, name)
			assert.NotNil(t, res, name)
		}
	}
	assertReadiness()

	// First resource becomes ready
	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 1}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-1", "test-resource-2"}, enqueued)

	expectedVisibility["test-resource-2"] = true
	assertReadiness()
	assertReadiness()

	// Third resource becomes ready, skipping the second
	//
	// This shouldn't actually be possible in real life.
	// The test exists only to avoid undefined behavior.
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 3}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-3", "test-resource-4"}, enqueued)

	expectedVisibility["test-resource-4"] = true
	assertReadiness()

	// Nothing is enqueued because the resource is already ready
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 3}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Nil(t, enqueued)

	// It is enqueued again when the status changes
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 3}, &apiv1.ResourceState{Ready: &metav1.Time{}, Reconciled: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-3"}, enqueued)
}

func TestTreeDeletion(t *testing.T) {
	var b treeBuilder
	b.Add(&Resource{
		Ref:                newTestRef("test-resource-1"),
		readinessGroup:     1,
		ManifestRef:        ManifestRef{Index: 1},
		parsed:             &unstructured.Unstructured{},
		compositionDeleted: true,
	})
	b.Add(&Resource{
		Ref:                newTestRef("test-resource-3"),
		readinessGroup:     3,
		ManifestRef:        ManifestRef{Index: 3},
		parsed:             &unstructured.Unstructured{},
		compositionDeleted: true,
	})
	b.Add(&Resource{
		Ref:                newTestRef("test-resource-2"),
		readinessGroup:     2,
		ManifestRef:        ManifestRef{Index: 2},
		parsed:             &unstructured.Unstructured{},
		compositionDeleted: true,
	})
	tree := b.Build()

	// All resources are seen, but only one is ready
	var enqueued []string
	for i := 1; i < 4; i++ {
		state := &apiv1.ResourceState{}
		if i == 1 {
			state.Ready = &metav1.Time{}
		}
		tree.UpdateState(ManifestRef{Index: i}, state, func(r Ref) {
			enqueued = append(enqueued, r.Name)
		})
	}
	assert.ElementsMatch(t, []string{"test-resource-1", "test-resource-2", "test-resource-3"}, enqueued)

	for i := 1; i < 4; i++ {
		res, visible, found := tree.Get(newTestRef(fmt.Sprintf("test-resource-%d", i)))
		assert.True(t, found)
		assert.True(t, visible)
		require.NotNil(t, res)

		snap, err := res.Snapshot(t.Context(), &apiv1.Composition{}, nil)
		require.NoError(t, err)
		assert.True(t, snap.Deleted())
	}
}

func TestTreeRefConflicts(t *testing.T) {
	var b treeBuilder
	b.Add(&Resource{
		Ref:          newTestRef("test-resource"),
		manifestHash: []byte("b"),
	})
	b.Add(&Resource{
		Ref:          newTestRef("test-resource"),
		manifestHash: []byte("a"),
	})
	tree := b.Build()

	res, visible, found := tree.Get(newTestRef("test-resource"))
	assert.True(t, found)
	assert.True(t, visible)
	assert.Equal(t, "b", string(res.manifestHash))
}
func TestShadowGetReturnsNotFound(t *testing.T) {
	// Shadow resources should not be returned by Get
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:         newTestRef("shadow-resource"),
		ManifestRef: ManifestRef{Index: 0},
	})
	b.Add(&Resource{
		Ref:         newTestRef("real-resource"),
		ManifestRef: ManifestRef{Index: 1},
	})
	tree := b.Build()

	// Shadow resource is not found
	res, visible, found := tree.Get(newTestRef("shadow-resource"))
	assert.Nil(t, res)
	assert.False(t, visible)
	assert.False(t, found)

	// Real resource is found and visible
	res, visible, found = tree.Get(newTestRef("real-resource"))
	assert.NotNil(t, res)
	assert.True(t, visible)
	assert.True(t, found)
}

func TestShadowUpdateStateDoesNotEnqueueSelf(t *testing.T) {
	// Shadow resources should NOT enqueue themselves on state change, but should still track state
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:         newTestRef("shadow-resource"),
		ManifestRef: ManifestRef{Index: 0},
	})
	tree := b.Build()

	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Empty(t, enqueued, "shadow resource should not enqueue itself")
}

func TestShadowDeletionGroupUnblocksDependents(t *testing.T) {
	// Core cross-reconciler scenario:
	// Shadow resource at deletion-group -1 (e.g. CRD managed by other reconciler)
	// Real resource at deletion-group 0 (e.g. Deployment managed by this reconciler)
	// When the shadow's Deleted status transitions, the real resource should be unblocked.
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:                newTestRef("shadow-crd"),
		deletionGroup:      ptr.To(-1),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 0},
	})
	b.Add(&Resource{
		Ref:                newTestRef("real-deployment"),
		deletionGroup:      ptr.To(0),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 1},
	})
	tree := b.Build()

	// Real resource should be blocked (depends on shadow)
	res, visible, found := tree.Get(newTestRef("real-deployment"))
	assert.NotNil(t, res)
	assert.False(t, visible, "real resource should be blocked by shadow dependency")
	assert.True(t, found)

	// Shadow resource should not be found via Get
	res, visible, found = tree.Get(newTestRef("shadow-crd"))
	assert.Nil(t, res)
	assert.False(t, visible)
	assert.False(t, found)

	// Simulate the other reconciler deleting the shadow resource and marking state as Deleted
	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	// Shadow should NOT enqueue itself, but SHOULD enqueue the dependent real resource
	assert.ElementsMatch(t, []string{"real-deployment"}, enqueued)

	// Now the real resource should be visible (unblocked)
	res, visible, found = tree.Get(newTestRef("real-deployment"))
	assert.NotNil(t, res)
	assert.True(t, visible, "real resource should be unblocked after shadow deletion")
	assert.True(t, found)
}

func TestShadowReadinessGroupUnblocksDependents(t *testing.T) {
	// Readiness scenario (non-deletion):
	// Shadow resource at readiness-group 0
	// Real resource at readiness-group 1
	// When the shadow becomes ready, the real resource should be unblocked.
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:            newTestRef("shadow-infra"),
		readinessGroup: 0,
		ManifestRef:    ManifestRef{Index: 0},
	})
	b.Add(&Resource{
		Ref:            newTestRef("real-app"),
		readinessGroup: 1,
		ManifestRef:    ManifestRef{Index: 1},
	})
	tree := b.Build()

	// Real resource should be blocked
	_, visible, found := tree.Get(newTestRef("real-app"))
	assert.False(t, visible, "should be blocked by shadow dependency")
	assert.True(t, found)

	// Shadow becomes ready
	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	// Shadow should NOT enqueue itself, but SHOULD unblock and enqueue the real resource
	assert.ElementsMatch(t, []string{"real-app"}, enqueued)

	// Now the real resource should be visible
	_, visible, found = tree.Get(newTestRef("real-app"))
	assert.True(t, visible, "should be unblocked after shadow ready")
	assert.True(t, found)
}

func TestShadowMultipleDeletionGroups(t *testing.T) {
	// Multiple shadows at different deletion groups should chain correctly:
	// shadow-a (group -2) -> shadow-b (group -1) -> real (group 0)
	// real should only become visible after both shadows report Deleted.
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:                newTestRef("shadow-a"),
		deletionGroup:      ptr.To(-2),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 0},
	})
	b.AddShadow(&Resource{
		Ref:                newTestRef("shadow-b"),
		deletionGroup:      ptr.To(-1),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 1},
	})
	b.Add(&Resource{
		Ref:                newTestRef("real-resource"),
		deletionGroup:      ptr.To(0),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 2},
	})
	tree := b.Build()

	// Real resource blocked
	_, visible, _ := tree.Get(newTestRef("real-resource"))
	assert.False(t, visible)

	// Delete shadow-a
	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"shadow-b"}, enqueued, "shadow-a deletion should unblock shadow-b")

	// Real resource still blocked (shadow-b not yet deleted)
	_, visible, _ = tree.Get(newTestRef("real-resource"))
	assert.False(t, visible, "real should still be blocked by shadow-b")

	// Delete shadow-b
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 1}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"real-resource"}, enqueued, "shadow-b deletion should unblock real resource")

	// Real resource now visible
	_, visible, _ = tree.Get(newTestRef("real-resource"))
	assert.True(t, visible)
}

func TestShadowAndRealMixedDeletionGroups(t *testing.T) {
	// Mix of shadow and real resources in the same deletion group chain:
	// real-a (group 0) -> shadow-b (group 1) -> real-c (group 2)
	var b treeBuilder
	b.Add(&Resource{
		Ref:                newTestRef("real-a"),
		deletionGroup:      ptr.To(0),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 0},
	})
	b.AddShadow(&Resource{
		Ref:                newTestRef("shadow-b"),
		deletionGroup:      ptr.To(1),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 1},
	})
	b.Add(&Resource{
		Ref:                newTestRef("real-c"),
		deletionGroup:      ptr.To(2),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 2},
	})
	tree := b.Build()

	// real-a is visible (no deps), shadow-b and real-c are blocked
	_, visible, found := tree.Get(newTestRef("real-a"))
	assert.True(t, visible)
	assert.True(t, found)

	_, visible, _ = tree.Get(newTestRef("real-c"))
	assert.False(t, visible, "real-c should be blocked by shadow-b")

	// Delete real-a -> unblocks shadow-b
	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Contains(t, enqueued, "shadow-b")

	// real-c still blocked (shadow-b not deleted yet)
	_, visible, _ = tree.Get(newTestRef("real-c"))
	assert.False(t, visible)

	// Delete shadow-b -> unblocks real-c
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 1}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"real-c"}, enqueued)

	_, visible, _ = tree.Get(newTestRef("real-c"))
	assert.True(t, visible, "real-c should be unblocked after shadow-b deletion")
}

func TestShadowNoDeletionGroupIsIndependent(t *testing.T) {
	// A shadow without a deletion group should not block anything during deletion
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:                newTestRef("shadow-no-group"),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 0},
	})
	b.Add(&Resource{
		Ref:                newTestRef("real-with-group"),
		deletionGroup:      ptr.To(0),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 1},
	})
	tree := b.Build()

	// Real resource has no dependency on the shadow (shadow has no group)
	_, visible, found := tree.Get(newTestRef("real-with-group"))
	assert.True(t, visible, "real resource should not be blocked by groupless shadow")
	assert.True(t, found)
}

func TestShadowRepeatedUpdateStateNoSelfEnqueue(t *testing.T) {
	// Repeated state updates to a shadow should never enqueue the shadow itself
	var b treeBuilder
	b.AddShadow(&Resource{
		Ref:                newTestRef("shadow-resource"),
		deletionGroup:      ptr.To(0),
		compositionDeleted: true,
		ManifestRef:        ManifestRef{Index: 0},
	})
	tree := b.Build()

	// First update
	var enqueued []string
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Empty(t, enqueued, "shadow should never self-enqueue")

	// Second update with change
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Reconciled: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Empty(t, enqueued, "shadow should never self-enqueue even on state change")

	// Third update with Deleted
	enqueued = nil
	tree.UpdateState(ManifestRef{Index: 0}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Empty(t, enqueued, "shadow with no dependents should enqueue nothing")
}

func TestIndexedResourceBacktracks(t *testing.T) {
	baseGVK := schema.GroupVersionKind{Group: "test.group", Version: "v1", Kind: "TestKind"}

	newIdx := func(name string, group int) *indexedResource {
		return &indexedResource{
			Resource: &Resource{
				Ref:            newTestRef(name),
				GVK:            baseGVK,
				readinessGroup: group,
			},
			PendingDependencies: map[Ref]struct{}{},
			Dependents:          map[Ref]*indexedResource{},
		}
	}

	t.Run("no dependents", func(t *testing.T) {
		ir := newIdx("a", 1)
		assert.False(t, ir.Backtracks())
	})

	t.Run("dependent with different GVK", func(t *testing.T) {
		ir := newIdx("a", 1)
		dep := &indexedResource{
			Resource: &Resource{
				Ref:            newTestRef("a"),
				GVK:            schema.GroupVersionKind{Group: "other.group", Version: "v1", Kind: "OtherKind"},
				readinessGroup: 2,
			},
			PendingDependencies: map[Ref]struct{}{},
			Dependents:          map[Ref]*indexedResource{},
		}
		ir.Dependents[dep.Resource.Ref] = dep
		assert.False(t, ir.Backtracks())
	})

	t.Run("dependent with same GVK and Ref, but has pending dependencies", func(t *testing.T) {
		ir := newIdx("a", 1)
		dep := newIdx("a", 2)
		dep.PendingDependencies[newTestRef("other")] = struct{}{}
		ir.Dependents[dep.Resource.Ref] = dep
		assert.False(t, ir.Backtracks())
	})

	t.Run("dependent with same GVK and Ref, no pending dependencies", func(t *testing.T) {
		ir := newIdx("a", 1)
		dep := newIdx("a", 2)
		ir.Dependents[dep.Resource.Ref] = dep
		assert.True(t, ir.Backtracks())
	})

	t.Run("multiple dependents, only one triggers backtrack", func(t *testing.T) {
		ir := newIdx("a", 1)
		dep1 := newIdx("a", 2)
		dep2 := newIdx("b", 2)
		ir.Dependents[dep1.Resource.Ref] = dep1
		ir.Dependents[dep2.Resource.Ref] = dep2
		assert.True(t, ir.Backtracks())
	})

	t.Run("multiple dependents, none triggers backtrack", func(t *testing.T) {
		ir := newIdx("a", 1)
		dep1 := newIdx("b", 2)
		dep2 := newIdx("c", 2)
		ir.Dependents[dep1.Resource.Ref] = dep1
		ir.Dependents[dep2.Resource.Ref] = dep2
		assert.False(t, ir.Backtracks())
	})
}
