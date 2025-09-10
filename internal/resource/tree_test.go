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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
					Ref:           newTestRef("test-deletion-high"),
					deletionGroup: 4, // deleted first
				},
				{
					Ref:           newTestRef("test-deletion-medium"),
					deletionGroup: 2,
				},
				{
					Ref: newTestRef("test-deletion-default"),
					// deletionGroup defaults to 0
				},
				{
					Ref:           newTestRef("test-deletion-low"),
					deletionGroup: -2, // deleted last
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

	res, visible, _, found := tree.Get(newTestRef("foobar"))
	assert.False(t, found, "404 case")
	assert.False(t, visible)
	assert.Nil(t, res)

	tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: 100}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {}) // it doesn't panic

	// Default readiness
	expectedVisibility := map[string]bool{"test-resource-1": true}
	assertReadiness := func() {
		for _, name := range names {
			res, visible, _, found := tree.Get(newTestRef(name))
			assert.True(t, found, name)
			assert.Equal(t, expectedVisibility[name], visible, name)
			assert.NotNil(t, res, name)
		}
	}
	assertReadiness()

	// First resource becomes ready
	var enqueued []string
	tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: 1}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
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
	tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: 3}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-3", "test-resource-4"}, enqueued)

	expectedVisibility["test-resource-4"] = true
	assertReadiness()

	// Nothing is enqueued because the resource is already ready
	enqueued = nil
	tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: 3}, &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Nil(t, enqueued)

	// It is enqueued again when the status changes
	enqueued = nil
	tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: 3}, &apiv1.ResourceState{Ready: &metav1.Time{}, Reconciled: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-3"}, enqueued)
}

func TestTreeDeletion(t *testing.T) {
	var b treeBuilder
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
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-4"),
		readinessGroup: 2,
		deletionGroup:  -4,
		ManifestRef:    ManifestRef{Index: 4},
	})

	tree := b.Build()

	// All resources are seen, but only one is ready
	var enqueued []string
	for i := 1; i < 4; i++ {
		state := &apiv1.ResourceState{}
		if i == 1 {
			state.Ready = &metav1.Time{}
		}
		tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: i}, state, func(r Ref) {
			enqueued = append(enqueued, r.Name)
		})
	}
	assert.ElementsMatch(t, []string{"test-resource-1", "test-resource-2", "test-resource-2", "test-resource-3", "test-resource-4"}, enqueued)

	// The third resource should not be visible yet because it's readiness group is still blocked
	_, visible, strictDelete, found := tree.Get(newTestRef("test-resource-3"))
	assert.False(t, visible)
	assert.True(t, strictDelete)
	assert.True(t, found)

	// Deleting the composition should enqueue every item except those blocked by earlier deletion groups
	enqueued = nil
	for i := 1; i < 4; i++ {
		comp := &apiv1.Composition{}
		comp.DeletionTimestamp = &metav1.Time{}
		tree.UpdateState(comp, ManifestRef{Index: i}, &apiv1.ResourceState{}, func(r Ref) {
			enqueued = append(enqueued, r.Name)
		})
	}
	assert.ElementsMatch(t, []string{"test-resource-1", "test-resource-2", "test-resource-3"}, enqueued)

	for _, r := range tree.byRef {
		r.CompositionDeleting = true
	}

	// ...but only once
	enqueued = nil
	for i := 1; i < 3; i++ {
		comp := &apiv1.Composition{}
		comp.DeletionTimestamp = &metav1.Time{}
		tree.UpdateState(comp, ManifestRef{Index: i}, &apiv1.ResourceState{}, func(r Ref) {
			enqueued = append(enqueued, r.Name)
		})
	}
	assert.Nil(t, enqueued)

	// The third resource should be visible now
	_, visible, strictDelete, found = tree.Get(newTestRef("test-resource-3"))
	assert.True(t, visible)
	assert.True(t, strictDelete)
	assert.True(t, found)

	// The fourth resource should not be visible now
	_, visible, strictDelete, found = tree.Get(newTestRef("test-resource-4"))
	assert.False(t, visible)
	assert.False(t, strictDelete)
	assert.True(t, found)

	// Observe the first three resources in a deleted state
	enqueued = nil
	for i := 1; i < 4; i++ {
		state := &apiv1.ResourceState{Deleted: true}
		tree.UpdateState(&apiv1.Composition{}, ManifestRef{Index: i}, state, func(r Ref) {
			enqueued = append(enqueued, r.Name)
		})
	}
	// When resources 1-3 are deleted, they get enqueued themselves plus resource 4 becomes unblocked
	// (resource 4 gets enqueued 3 times since each deleted resource triggers it independently)
	expected := []string{"test-resource-1", "test-resource-2", "test-resource-3", "test-resource-4", "test-resource-4", "test-resource-4"}
	assert.ElementsMatch(t, expected, enqueued)

	// The fourth resource should be visible now
	_, visible, strictDelete, found = tree.Get(newTestRef("test-resource-4"))
	assert.True(t, visible)
	assert.False(t, strictDelete)
	assert.True(t, found)
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

	res, visible, _, found := tree.Get(newTestRef("test-resource"))
	assert.True(t, found)
	assert.True(t, visible)
	assert.Equal(t, "b", string(res.manifestHash))
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

func TestTreeDeletionGroups(t *testing.T) {
	resources := []*Resource{
		{
			Ref:           newTestRef("high-priority-1"),
			deletionGroup: 10, // deleted first
			ManifestRef:   ManifestRef{Index: 1},
		},
		{
			Ref:           newTestRef("high-priority-2"),
			deletionGroup: 10, // deleted first (same group)
			ManifestRef:   ManifestRef{Index: 2},
		},
		{
			Ref:           newTestRef("medium-priority"),
			deletionGroup: 5, // deleted after high-priority
			ManifestRef:   ManifestRef{Index: 3},
		},
		{
			Ref:           newTestRef("default-priority"),
			deletionGroup: 0, // deleted after medium-priority (default)
			ManifestRef:   ManifestRef{Index: 4},
		},
		{
			Ref:           newTestRef("low-priority"),
			deletionGroup: -5, // deleted last
			ManifestRef:   ManifestRef{Index: 5},
		},
	}

	var b treeBuilder
	for _, r := range resources {
		b.Add(r)
	}

	tree := b.Build()

	// Mark composition as deleting
	comp := &apiv1.Composition{}
	comp.DeletionTimestamp = &metav1.Time{}

	// Initially, only high-priority resources (group 10) should be visible
	for i := range resources {
		tree.UpdateState(comp, ManifestRef{Index: i + 1}, &apiv1.ResourceState{}, func(ref Ref) {})
	}

	assertVisibility := func(expected map[string]bool) {
		t.Helper()
		for _, r := range resources {
			_, visible, _, found := tree.Get(r.Ref)
			assert.True(t, found, "resource %s should be found", r.Ref.Name)
			expectedVis := expected[r.Ref.Name]
			assert.Equal(t, expectedVis, visible, "resource %s visibility", r.Ref.Name)
		}
	}

	// Only high-priority resources (deletion group 10) should be visible initially
	assertVisibility(map[string]bool{
		"high-priority-1":  true,
		"high-priority-2":  true,
		"medium-priority":  false,
		"default-priority": false,
		"low-priority":     false,
	})

	// Delete high-priority resources
	var enqueued []string
	for i := 1; i <= 2; i++ {
		tree.UpdateState(comp, ManifestRef{Index: i}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
			enqueued = append(enqueued, r.Name)
		})
	}

	// Medium-priority should become visible and get enqueued
	assert.Contains(t, enqueued, "medium-priority")
	assertVisibility(map[string]bool{
		"high-priority-1":  true,
		"high-priority-2":  true,
		"medium-priority":  true,
		"default-priority": false,
		"low-priority":     false,
	})

	// Delete medium-priority resource
	enqueued = nil
	tree.UpdateState(comp, ManifestRef{Index: 3}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})

	// Default-priority should become visible and get enqueued
	assert.Contains(t, enqueued, "default-priority")
	assertVisibility(map[string]bool{
		"high-priority-1":  true,
		"high-priority-2":  true,
		"medium-priority":  true,
		"default-priority": true,
		"low-priority":     false,
	})

	// Delete default-priority resource
	enqueued = nil
	tree.UpdateState(comp, ManifestRef{Index: 4}, &apiv1.ResourceState{Deleted: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})

	// Low-priority should become visible and get enqueued
	assert.Contains(t, enqueued, "low-priority")
	assertVisibility(map[string]bool{
		"high-priority-1":  true,
		"high-priority-2":  true,
		"medium-priority":  true,
		"default-priority": true,
		"low-priority":     true,
	})
}
