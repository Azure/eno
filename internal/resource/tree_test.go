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
