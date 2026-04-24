package scheduling

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestBuildReadySet(t *testing.T) {
	comps := &apiv1.CompositionList{
		Items: []apiv1.Composition{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ready-comp", Namespace: "ns1"},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{
						Ready: ptr.To(metav1.Now()),
					},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "not-ready-comp", Namespace: "ns1"},
				Status: apiv1.CompositionStatus{
					CurrentSynthesis: &apiv1.Synthesis{},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "no-synthesis", Namespace: "ns2"},
			},
		},
	}

	readySet := buildReadySet(comps)

	assert.True(t, readySet["ns1/ready-comp"])
	assert.False(t, readySet["ns1/not-ready-comp"])
	assert.False(t, readySet["ns2/no-synthesis"])
	assert.Len(t, readySet, 1)
}

func TestBuildExistsSet(t *testing.T) {
	comps := &apiv1.CompositionList{
		Items: []apiv1.Composition{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-a", Namespace: "ns1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-b", Namespace: "ns1"},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "comp-c", Namespace: "ns2"},
			},
		},
	}

	existsSet := buildExistsSet(comps)

	assert.True(t, existsSet["ns1/comp-a"])
	assert.True(t, existsSet["ns1/comp-b"])
	assert.True(t, existsSet["ns2/comp-c"])
	assert.False(t, existsSet["ns1/nonexistent"])
	assert.Len(t, existsSet, 3)
}

func TestAreDependenciesReady(t *testing.T) {
	readySet := map[string]bool{
		"ns1/dep-a": true,
		"ns1/dep-b": true,
	}

	tests := []struct {
		name     string
		comp     *apiv1.Composition
		expected bool
	}{
		{
			name: "no dependencies",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
			},
			expected: true,
		},
		{
			name: "all deps ready",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "dep-a", Namespace: "ns1"},
						{Name: "dep-b", Namespace: "ns1"},
					},
				},
			},
			expected: true,
		},
		{
			name: "required dep not ready",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "dep-a", Namespace: "ns1"},
						{Name: "dep-missing", Namespace: "ns1"},
					},
				},
			},
			expected: false,
		},
		{
			name: "all deps required - one missing blocks",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "dep-a", Namespace: "ns1"},
						{Name: "dep-missing", Namespace: "ns1"},
					},
				},
			},
			expected: false,
		},
		{
			name: "cross-namespace dep ready",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "other-ns"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "dep-a", Namespace: "ns1"},
					},
				},
			},
			expected: true,
		},
		{
			name: "cross-namespace dep not ready",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "other-ns"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "dep-a", Namespace: "other-ns"}, // explicit other-ns, which isn't in readySet
					},
				},
			},
			expected: false,
		},
		{
			name: "all deps missing blocks",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "nonexistent-1", Namespace: "ns1"},
						{Name: "nonexistent-2", Namespace: "ns1"},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := areDependenciesReady(tt.comp, readySet)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTopoSortCompositions(t *testing.T) {
	tests := []struct {
		name           string
		compositions   []apiv1.Composition
		expectedOrder  []string // namespace/name keys in expected order
		expectedCyclic []string
	}{
		{
			name:           "empty",
			compositions:   nil,
			expectedOrder:  []string{},
			expectedCyclic: nil,
		},
		{
			name: "single no deps",
			compositions: []apiv1.Composition{
				{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}},
			},
			expectedOrder:  []string{"ns/a"},
			expectedCyclic: nil,
		},
		{
			name: "linear chain A->B->C",
			compositions: []apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b", Namespace: "ns"}},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a", Namespace: "ns"}},
					},
				},
				{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"}},
			},
			expectedOrder:  []string{"ns/a", "ns/b", "ns/c"},
			expectedCyclic: nil,
		},
		{
			name: "diamond A->B,A->C,B->D,C->D",
			compositions: []apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "d", Namespace: "ns"}},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{
							{Name: "b", Namespace: "ns"},
							{Name: "c", Namespace: "ns"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "d", Namespace: "ns"}},
					},
				},
				{ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"}},
			},
			expectedOrder:  []string{"ns/d", "ns/b", "ns/c", "ns/a"},
			expectedCyclic: nil,
		},
		{
			name: "simple cycle A<->B",
			compositions: []apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b", Namespace: "ns"}},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a", Namespace: "ns"}},
					},
				},
			},
			expectedOrder:  []string{},
			expectedCyclic: []string{"ns/a", "ns/b"},
		},
		{
			name: "cycle with independent node",
			compositions: []apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b", Namespace: "ns"}},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a", Namespace: "ns"}},
					},
				},
				{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}},
			},
			expectedOrder:  []string{"ns/c"},
			expectedCyclic: []string{"ns/a", "ns/b"},
		},
		{
			name: "cross-namespace dependencies",
			compositions: []apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "prod"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "db", Namespace: "infra"}},
					},
				},
				{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "infra"}},
			},
			expectedOrder:  []string{"infra/db", "prod/app"},
			expectedCyclic: nil,
		},
		{
			name: "self loop",
			compositions: []apiv1.Composition{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a", Namespace: "ns"}},
					},
				},
			},
			expectedOrder:  []string{},
			expectedCyclic: []string{"ns/a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted, cyclicSet := topoSortCompositions(tt.compositions)

			var sortedKeys []string
			for _, comp := range sorted {
				sortedKeys = append(sortedKeys, comp.Namespace+"/"+comp.Name)
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
