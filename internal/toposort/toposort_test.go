package toposort

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type node struct {
	name string
	deps []string
}

func TestTopologySort(t *testing.T) {
	tests := []struct {
		name           string
		items          []node
		expectedOrder  []string
		expectedCyclic []string
	}{
		{
			name:           "empty input",
			items:          nil,
			expectedOrder:  []string{},
			expectedCyclic: nil,
		},
		{
			name:           "single node no deps",
			items:          []node{{name: "a"}},
			expectedOrder:  []string{"a"},
			expectedCyclic: nil,
		},
		{
			name: "two independent nodes",
			items: []node{
				{name: "b"},
				{name: "a"},
			},
			expectedOrder:  []string{"a", "b"}, // sorted alphabetically by Kahn's queue
			expectedCyclic: nil,
		},
		{
			name: "linear chain a->b->c",
			items: []node{
				{name: "c", deps: []string{"b"}},
				{name: "b", deps: []string{"a"}},
				{name: "a"},
			},
			expectedOrder:  []string{"a", "b", "c"},
			expectedCyclic: nil,
		},
		{
			name: "diamond",
			items: []node{
				{name: "a", deps: []string{"b", "c"}},
				{name: "b", deps: []string{"d"}},
				{name: "c", deps: []string{"d"}},
				{name: "d"},
			},
			expectedOrder:  []string{"d", "b", "c", "a"},
			expectedCyclic: nil,
		},
		{
			name: "simple cycle",
			items: []node{
				{name: "a", deps: []string{"b"}},
				{name: "b", deps: []string{"a"}},
			},
			expectedOrder:  []string{},
			expectedCyclic: []string{"a", "b"},
		},
		{
			name: "self loop",
			items: []node{
				{name: "a", deps: []string{"a"}},
			},
			expectedOrder:  []string{},
			expectedCyclic: []string{"a"},
		},
		{
			name: "cycle with independent nodes",
			items: []node{
				{name: "x", deps: []string{"y"}},
				{name: "y", deps: []string{"x"}},
				{name: "z"},
			},
			expectedOrder:  []string{"z"},
			expectedCyclic: []string{"x", "y"},
		},
		{
			name: "three node cycle",
			items: []node{
				{name: "a", deps: []string{"b"}},
				{name: "b", deps: []string{"c"}},
				{name: "c", deps: []string{"a"}},
			},
			expectedOrder:  []string{},
			expectedCyclic: []string{"a", "b", "c"},
		},
		{
			name: "dep references non-existent node",
			items: []node{
				{name: "a", deps: []string{"phantom"}},
			},
			expectedOrder:  []string{},
			expectedCyclic: []string{"a"},
		},
		{
			name: "complex mixed graph",
			items: []node{
				{name: "e"},
				{name: "d", deps: []string{"e"}},
				{name: "a", deps: []string{"b"}},
				{name: "b", deps: []string{"c"}},
				{name: "c", deps: []string{"a"}}, // a,b,c form a cycle
			},
			expectedOrder:  []string{"e", "d"},
			expectedCyclic: []string{"a", "b", "c"},
		},
	}

	keyFn := func(n *node) string { return n.name }
	depsFn := func(n *node) []string { return n.deps }

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted, cyclicSet := TopologySort(tt.items, keyFn, depsFn)

			var sortedKeys []string
			for _, n := range sorted {
				sortedKeys = append(sortedKeys, n.name)
			}
			if len(tt.expectedOrder) == 0 {
				assert.Empty(t, sortedKeys)
			} else {
				assert.Equal(t, tt.expectedOrder, sortedKeys)
			}

			if tt.expectedCyclic == nil {
				assert.Empty(t, cyclicSet)
			} else {
				assert.Len(t, cyclicSet, len(tt.expectedCyclic))
				for _, key := range tt.expectedCyclic {
					assert.True(t, cyclicSet[key], "expected %s in cyclic set", key)
				}
			}
		})
	}
}
