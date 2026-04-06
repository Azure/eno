package symphony

import (
	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/toposort"
)

// topoSortVariations returns variations in topological order (dependency first)
// and a set of syntheiszer names that are part of dependency cycle
// Uses the generic Kahn's algorithm O(V+E)
func topoSortVariations(variations []apiv1.Variation) (sorted []apiv1.Variation, cyclicSynths map[string]bool) {
	return toposort.TopologySort(variations,
		func(variation *apiv1.Variation) string { return variation.Synthesizer.Name },
		func(variation *apiv1.Variation) []string {
			var deps []string
			for _, dep := range variation.DependsOn {
				if dep.Synthesizer != "" { // a safety guard to ensure no empty synthesizers will be added
					deps = append(deps, dep.Synthesizer)
				}
			}
			return deps
		},
	)
}
