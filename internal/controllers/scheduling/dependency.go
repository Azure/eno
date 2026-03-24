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

// buildComsByKey creates a map of namespace/name -> *composition for cycle detection
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
		ns := dep.Namespace
		if ns == "" {
			ns = comp.GetNamespace()
		}
		key := path.Join(ns, dep.Name)
		if !readySet[key] { // dependency is not ready
			if dep.Optional {
				continue
			}
			return false
		}
	}
	return true
}

// detectAllCycles returns a set of compositions keys that are part of any cycle.
// Runs a single DFS pass over the entire graph: O(N + E) run time
func detectAllCycles(allComps map[string]*apiv1.Composition) map[string]bool {
	cyclic := map[string]bool{}
	visited := map[string]bool{}
	stack := map[string]bool{}

	var dfs func(key string)
	dfs = func(key string) {
		if visited[key] {
			return
		}

		visited[key] = true
		stack[key] = true

		if comp, ok := allComps[key]; ok {
			for _, dep := range comp.Spec.DependsOn {
				ns := dep.Namespace
				if ns == "" {
					ns = comp.GetNamespace()
				}
				depKey := path.Join(ns, dep.Name)
				if stack[depKey] {
					cyclic[depKey] = true
					cyclic[key] = true
					continue
				}
				dfs(depKey)
				if cyclic[depKey] {
					cyclic[key] = true
				}
			}
		}
		delete(stack, key)
	}
	for key := range allComps {
		dfs(key)
	}
	return cyclic
}
