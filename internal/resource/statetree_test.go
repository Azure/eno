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

func TestStateTreeBuilderSanity(t *testing.T) {
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
					ReadinessGroup: -2,
				},
				{
					Ref:            newTestRef("test-1"),
					ReadinessGroup: 1,
				},
				{
					Ref: newTestRef("test-0"),
				},
				{
					Ref:            newTestRef("test-4"),
					ReadinessGroup: 4,
				},
			},
		},
		{
			Name: "several-overlapping-groups",
			Resources: []*Resource{
				{
					Ref:            newTestRef("test-1"),
					ReadinessGroup: 4,
				},
				{
					Ref:            newTestRef("test-2-a"),
					ReadinessGroup: 8,
				},
				{
					Ref:            newTestRef("test-2-b"),
					ReadinessGroup: 8,
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
					ReadinessGroup: 5,
				},
				{
					Ref:              newTestRef("test-crd"),
					DefinedGroupKind: &schema.GroupKind{Group: "test.group", Kind: "TestCRDKind"},
					ReadinessGroup:   3,
				},
				{
					Ref:            newTestRef("also-not-a-crd"),
					ReadinessGroup: 10,
				},
				{
					Ref:            newTestRef("not-a-crd"),
					ReadinessGroup: 1,
				},
			},
		},
		{
			Name: "both-crd-and-cr-conflict",
			Resources: []*Resource{
				{
					Ref:            newTestRef("test-cr"),
					GVK:            schema.GroupVersionKind{Group: "test.group", Version: "v1", Kind: "TestCRDKind"},
					ReadinessGroup: 3,
				},
				{
					Ref:              newTestRef("test-crd"),
					DefinedGroupKind: &schema.GroupKind{Group: "test.group", Kind: "TestCRDKind"},
					ReadinessGroup:   5,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			var b stateTreeBuilder
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

func TestStateTreeVisibility(t *testing.T) {
	var b stateTreeBuilder
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-4"),
		ReadinessGroup: 4,
	})
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-1"),
		ReadinessGroup: 1,
	})
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-3"),
		ReadinessGroup: 3,
	})
	b.Add(&Resource{
		Ref:            newTestRef("test-resource-2"),
		ReadinessGroup: 2,
	})
	names := []string{"test-resource-1", "test-resource-2", "test-resource-3", "test-resource-4"}

	tree := b.Build()

	res, visible, found := tree.Get(newTestRef("foobar"))
	assert.False(t, found, "404 case")
	assert.False(t, visible)
	assert.Nil(t, res)

	tree.UpdateState(newTestRef("foobar"), &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {}) // it doesn't panic

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
	tree.UpdateState(newTestRef("test-resource-1"), &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
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
	tree.UpdateState(newTestRef("test-resource-3"), &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-3", "test-resource-4"}, enqueued)

	expectedVisibility["test-resource-4"] = true
	assertReadiness()

	// Nothing is enqueued because the resource is already ready
	enqueued = nil
	tree.UpdateState(newTestRef("test-resource-3"), &apiv1.ResourceState{Ready: &metav1.Time{}}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.Nil(t, enqueued)

	// It is enqueued again when the status changes
	enqueued = nil
	tree.UpdateState(newTestRef("test-resource-3"), &apiv1.ResourceState{Ready: &metav1.Time{}, Reconciled: true}, func(r Ref) {
		enqueued = append(enqueued, r.Name)
	})
	assert.ElementsMatch(t, []string{"test-resource-3"}, enqueued)
}

func TestStateTreeRefConflicts(t *testing.T) {
	var b stateTreeBuilder
	b.Add(&Resource{
		Ref:      newTestRef("test-resource"),
		Manifest: &apiv1.Manifest{Manifest: "b"},
	})
	b.Add(&Resource{
		Ref:      newTestRef("test-resource"),
		Manifest: &apiv1.Manifest{Manifest: "a"},
	})

	tree := b.Build()

	res, visible, found := tree.Get(newTestRef("test-resource"))
	assert.True(t, found)
	assert.True(t, visible)
	assert.Equal(t, "b", res.Manifest.Manifest)
}
