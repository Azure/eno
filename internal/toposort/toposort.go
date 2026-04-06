// internal/toposort/toposort.go
package toposort

import "sort"

// TopoSort performs Kahn's algorithm on a slice of items.
// keyFn extracts a unique string key for each item.
// depsFn extracts the dependency keys for each item.
// Returns items in topological order (dependencies first) and a set of keys in cycles.
func TopologySort[T any](items []T, keyFn func(*T) string, depsFn func(*T) []string) (sorted []T, cyclic map[string]bool) {
	byKey := make(map[string]*T, len(items))
	inDegree := make(map[string]int, len(items))
	dependents := make(map[string][]string)

	for i := range items {
		key := keyFn(&items[i])
		byKey[key] = &items[i]
		if _, exists := inDegree[key]; !exists {
			inDegree[key] = 0
		}
		for _, dep := range depsFn(&items[i]) {
			dependents[dep] = append(dependents[dep], key)
			inDegree[key]++
		}
	}

	var queue []string
	for key, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, key)
		}
	}
	sort.Strings(queue)

	sorted = make([]T, 0, len(items))
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if node, ok := byKey[key]; ok {
			sorted = append(sorted, *node)
		}
		for _, dep := range dependents[key] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
				sort.Strings(queue)
			}
		}
	}

	cyclic = make(map[string]bool)
	for key, deg := range inDegree {
		if deg > 0 {
			cyclic[key] = true
		}
	}
	return sorted, cyclic
}
