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

func TestBuildCompsByKey(t *testing.T) {
	comps := &apiv1.CompositionList{
		Items: []apiv1.Composition{
			{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns2"}},
		},
	}

	m := buildCompsByKey(comps)

	assert.Len(t, m, 2)
	assert.Equal(t, "a", m["ns1/a"].Name)
	assert.Equal(t, "b", m["ns2/b"].Name)
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
						{Name: "dep-a"},
						{Name: "dep-b"},
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
						{Name: "dep-a"},
						{Name: "dep-missing"},
					},
				},
			},
			expected: false,
		},
		{
			name: "optional dep not ready",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "dep-a"},
						{Name: "dep-missing", Optional: true},
					},
				},
			},
			expected: true,
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
						{Name: "dep-a"}, // defaults to other-ns, which isn't in readySet
					},
				},
			},
			expected: false,
		},
		{
			name: "all deps optional and missing",
			comp: &apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Name: "comp", Namespace: "ns1"},
				Spec: apiv1.CompositionSpec{
					DependsOn: []apiv1.CompositionDependency{
						{Name: "nonexistent-1", Optional: true},
						{Name: "nonexistent-2", Optional: true},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := areDependenciesReady(tt.comp, readySet)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetectCycle(t *testing.T) {
	tests := []struct {
		name     string
		target   string // namespace/name of the composition to check
		comps    map[string]*apiv1.Composition
		expected bool
	}{
		{
			name:   "no dependencies, no cycle",
			target: "ns/a",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
				},
			},
			expected: false,
		},
		{
			name:   "linear chain, no cycle",
			target: "ns/c",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
				},
				"ns/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
				"ns/c": {
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b"}},
					},
				},
			},
			expected: false,
		},
		{
			name:   "simple A->B->A cycle",
			target: "ns/a",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b"}},
					},
				},
				"ns/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
			},
			expected: true,
		},
		{
			name:   "A->B->C->A cycle",
			target: "ns/a",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b"}},
					},
				},
				"ns/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "c"}},
					},
				},
				"ns/c": {
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
			},
			expected: true,
		},
		{
			name:   "diamond shape, no cycle",
			target: "ns/d",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
				},
				"ns/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
				"ns/c": {
					ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
				"ns/d": {
					ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{
							{Name: "b"},
							{Name: "c"},
						},
					},
				},
			},
			expected: false,
		},
		{
			name:   "self-loop",
			target: "ns/a",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
			},
			expected: true,
		},
		{
			name:   "dependency not in map (missing composition)",
			target: "ns/a",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "nonexistent"}},
					},
				},
			},
			expected: false,
		},
		{
			name:   "cross-namespace cycle",
			target: "ns1/a",
			comps: map[string]*apiv1.Composition{
				"ns1/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns1"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b", Namespace: "ns2"}},
					},
				},
				"ns2/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns2"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a", Namespace: "ns1"}},
					},
				},
			},
			expected: true,
		},
		{
			name:   "node not in cycle is not detected as cyclic",
			target: "ns/d",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b"}},
					},
				},
				"ns/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
				"ns/d": {
					ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"},
					// d has no deps — it's not part of the A<->B cycle
				},
			},
			expected: false,
		},
		{
			name:   "node depending on cyclic node is marked cyclic (conservative over-approximation)",
			target: "ns/d",
			comps: map[string]*apiv1.Composition{
				"ns/a": {
					ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "b"}},
					},
				},
				"ns/b": {
					ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
				"ns/d": {
					ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "ns"},
					Spec: apiv1.CompositionSpec{
						// D depends on A, which is part of the A<->B cycle.
						// D is not itself on the cycle, but is marked cyclic as a
						// conservative over-approximation since its dependency chain
						// is fundamentally broken.
						DependsOn: []apiv1.CompositionDependency{{Name: "a"}},
					},
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cyclicSet := detectAllCycles(tt.comps)
			result := cyclicSet[tt.target]
			assert.Equal(t, tt.expected, result)
		})
	}
}
