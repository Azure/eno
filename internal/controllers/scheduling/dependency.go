package scheduling

import (
	"path"

	apiv1 "github.com/Azure/eno/api/v1"
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

// buildCompsByKey creates a map of namespace/name -> *composition for cycle detection
func buildCompsByKey(comps *apiv1.CompositionList) map[string]*apiv1.Composition {
	m := make(map[string]*apiv1.Composition, len(comps.Items))
	for i := range comps.Items {
		comp := &comps.Items[i]
		m[path.Join(comp.GetNamespace(), comp.GetName())] = comp
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

// detectAllCycles returns a set of composition keys that are part of or depend on any cycle.
// This is a conservative over-approximation: nodes that transitively depend on a cyclic node
// are also marked cyclic. This is intentional — such compositions can never make progress
// (their dependency will never become Ready), and surfacing "CircularDependency" communicates
// that the dependency chain is fundamentally broken.
// Uses an iterative, stack-based DFS over the entire graph: O(N + E) run time.
func detectAllCycles(allComps map[string]*apiv1.Composition) map[string]bool {
	cyclic := map[string]bool{}
	color := map[string]int{} // 0=unvisited, 1=inProcess, 2=done

	for startKey := range allComps {
		if color[startKey] == 2 {
			continue
		}

		stack := []string{startKey}
		for len(stack) > 0 {
			key := stack[len(stack)-1]
			if color[key] == 0 {
				// first visit: mark in-progress, push unvisited children
				color[key] = 1
				if comp, ok := allComps[key]; ok {
					for _, dep := range comp.Spec.DependsOn {
						depKey := path.Join(dep.Namespace, dep.Name)
						if color[depKey] == 1 {
							cyclic[depKey] = true
							cyclic[key] = true
						} else if color[depKey] == 0 {
							stack = append(stack, depKey)
						} else if cyclic[depKey] {
							cyclic[key] = true
						}
					}
				}
			} else {
				// Second visit: pop, propagate cyclic from children, mark done
				stack = stack[:len(stack)-1]
				if color[key] == 1 {
					if comp, ok := allComps[key]; ok {
						for _, dep := range comp.Spec.DependsOn {
							depKey := path.Join(dep.Namespace, dep.Name)
							if cyclic[depKey] {
								cyclic[key] = true
							}
						}
					}
					color[key] = 2
				}
			}
		}
	}
	return cyclic
}
