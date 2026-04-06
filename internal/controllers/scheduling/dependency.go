package scheduling

import (
	"path"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/toposort"
)

// buildReadySet creates a set of namespace/name keys for compositions that
// have CurrentSynthesis.Ready != nil
func buildReadySet(comps *apiv1.CompositionList) map[string]bool {
	m := make(map[string]bool, len(comps.Items))
	for _, comp := range comps.Items {
		if comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.Ready != nil {
			m[path.Join(comp.GetNamespace(), comp.GetName())] = true
		}
	}
	return m
}

// areDependenciesReady checks if all required dependencies are ready.
func areDependenciesReady(comp *apiv1.Composition, readySet map[string]bool) bool {
	for _, dep := range comp.Spec.DependsOn {
		key := path.Join(dep.Namespace, dep.Name)
		if !readySet[key] { // dependency is not ready
			return false
		}
	}
	return true
}

// // topoSortVariations returns compositions in topological order (dependency first)
// and a set of syntheiszer names that are part of dependency cycle
// Uses the generic Kahn's algorithm O(V+E)
func topoSortCompositions(compositions []apiv1.Composition) (sortedComposition []apiv1.Composition, cyclicSet map[string]bool) {
	return toposort.TopologySort(compositions,
		func(comp *apiv1.Composition) string { return path.Join(comp.GetNamespace(), comp.GetName()) },
		func(comp *apiv1.Composition) []string {
			var deps []string
			for _, dep := range comp.Spec.DependsOn {
				deps = append(deps, path.Join(dep.Namespace, dep.Name))
			}
			return deps
		},
	)
}
