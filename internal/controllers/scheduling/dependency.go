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

// detectCycle returns true if the composition is part of a dependency cycle.
func detectCycle(comp *apiv1.Composition, allComps map[string]*apiv1.Composition) bool {
	visited := map[string]bool{}
	stack := map[string]bool{}
	return hasCycle(path.Join(comp.GetNamespace(), comp.GetName()), allComps, visited, stack)
}

func hasCycle(key string, allComps map[string]*apiv1.Composition, visited, stack map[string]bool) bool {
	if stack[key] {
		return true
	}
	if visited[key] {
		return false
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
			if hasCycle(depKey, allComps, visited, stack) {
				return true
			}
		}
	}
	delete(stack, key)
	return false
}
