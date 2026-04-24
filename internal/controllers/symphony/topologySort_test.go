package symphony

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func variation(synth string, deps ...string) apiv1.Variation {
	v := apiv1.Variation{Synthesizer: apiv1.SynthesizerRef{Name: synth}}
	for _, d := range deps {
		v.DependsOn = append(v.DependsOn, apiv1.VariationDependency{Synthesizer: d})
	}
	return v
}

func synthNames(variations []apiv1.Variation) []string {
	names := make([]string, len(variations))
	for i, v := range variations {
		names[i] = v.Synthesizer.Name
	}
	return names
}

func TestTopoSortVariations(t *testing.T) {
	tests := []struct {
		name          string
		variations    []apiv1.Variation
		wantOrder     []string
		wantCyclicLen int
	}{
		{
			name:          "empty",
			variations:    nil,
			wantOrder:     []string{},
			wantCyclicLen: 0,
		},
		{
			name:          "single no deps",
			variations:    []apiv1.Variation{variation("a")},
			wantOrder:     []string{"a"},
			wantCyclicLen: 0,
		},
		{
			name:          "two independent",
			variations:    []apiv1.Variation{variation("b"), variation("a")},
			wantOrder:     []string{"a", "b"}, // deterministic alphabetical
			wantCyclicLen: 0,
		},
		{
			name:          "linear chain A <- B <- C",
			variations:    []apiv1.Variation{variation("c", "b"), variation("a"), variation("b", "a")},
			wantOrder:     []string{"a", "b", "c"},
			wantCyclicLen: 0,
		},
		{
			name: "diamond A <- B, A <- C, B <- D, C <- D",
			variations: []apiv1.Variation{
				variation("d", "b", "c"),
				variation("b", "a"),
				variation("c", "a"),
				variation("a"),
			},
			wantOrder:     []string{"a", "b", "c", "d"},
			wantCyclicLen: 0,
		},
		{
			name:          "simple cycle A <-> B",
			variations:    []apiv1.Variation{variation("a", "b"), variation("b", "a")},
			wantOrder:     []string{},
			wantCyclicLen: 2,
		},
		{
			name: "cycle with independent node",
			variations: []apiv1.Variation{
				variation("a", "b"),
				variation("b", "a"),
				variation("c"),
			},
			wantOrder:     []string{"c"},
			wantCyclicLen: 2,
		},
		{
			name:          "self loop",
			variations:    []apiv1.Variation{variation("a", "a")},
			wantOrder:     []string{},
			wantCyclicLen: 1,
		},
		{
			name: "empty synthesizer in dep is ignored",
			variations: []apiv1.Variation{
				variation("a"),
				{
					Synthesizer: apiv1.SynthesizerRef{Name: "b"},
					DependsOn:   []apiv1.VariationDependency{{Synthesizer: ""}, {Synthesizer: "a"}},
				},
			},
			wantOrder:     []string{"a", "b"},
			wantCyclicLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted, cyclic := topoSortVariations(tt.variations)
			assert.Equal(t, tt.wantOrder, synthNames(sorted))
			require.Len(t, cyclic, tt.wantCyclicLen)
		})
	}
}
