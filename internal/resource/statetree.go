package resource

import (
	"encoding/json"
	"slices"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/emirpasic/gods/v2/trees/redblacktree"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type indexedResource struct {
	Resource            *Resource
	State               *apiv1.ResourceState
	PendingDependencies map[Ref]struct{}
	Dependents          map[Ref]*indexedResource
}

// stateTreeBuilder is used to index a set of resources into a stateTree.
type stateTreeBuilder struct {
	byRef        map[Ref]*indexedResource
	byGroup      *redblacktree.Tree[int, []*indexedResource]
	byDefiningGK map[schema.GroupKind]*indexedResource
	byGK         map[schema.GroupKind]*indexedResource
}

func (s *stateTreeBuilder) Add(resource *Resource) {
	// Initialize the builder
	if s.byRef == nil {
		s.byRef = map[Ref]*indexedResource{}
	}
	if s.byGroup == nil {
		s.byGroup = redblacktree.New[int, []*indexedResource]()
	}
	if s.byDefiningGK == nil {
		s.byDefiningGK = map[schema.GroupKind]*indexedResource{}
	}
	if s.byGK == nil {
		s.byGK = map[schema.GroupKind]*indexedResource{}
	}

	// Handle conflicting refs deterministically
	if existing, ok := s.byRef[resource.Ref]; ok && resource.Less(existing.Resource) {
		return
	}

	// Index the resource into the builder
	idx := &indexedResource{
		Resource:            resource,
		PendingDependencies: map[Ref]struct{}{},
		Dependents:          map[Ref]*indexedResource{},
	}
	s.byRef[resource.Ref] = idx
	current, _ := s.byGroup.Get(resource.ReadinessGroup)
	s.byGroup.Put(resource.ReadinessGroup, append(current, idx))
	s.byGK[resource.GVK.GroupKind()] = idx
	if resource.DefinedGroupKind != nil {
		s.byDefiningGK[*resource.DefinedGroupKind] = idx
	}
}

func (s *stateTreeBuilder) Build() *stateTree {
	t := &stateTree{byRef: s.byRef}

	for _, idx := range s.byRef {
		i := s.byGroup.IteratorAt(s.byGroup.GetNode(idx.Resource.ReadinessGroup))

		// CRs are dependent on their CRDs
		crd, ok := s.byDefiningGK[idx.Resource.GVK.GroupKind()]
		if ok {
			idx.PendingDependencies[crd.Resource.Ref] = struct{}{}
			crd.Dependents[idx.Resource.Ref] = idx
		}

		// Depend on any resources in the previous readiness group
		if i.Prev() {
			for _, dep := range i.Value() {
				idx.PendingDependencies[dep.Resource.Ref] = struct{}{}
			}
		}
		i.Next() //rewind

		// Any resources in the next readiness group depend on us
		if i.Next() && i.Key() > idx.Resource.ReadinessGroup {
			for _, cur := range i.Value() {
				idx.Dependents[cur.Resource.Ref] = cur
			}
		}
	}

	return t
}

// stateTree is an optimized, indexed representation of a set of resources.
// NOT CONCURRENCY SAFE.
type stateTree struct {
	byRef map[Ref]*indexedResource
}

// Get returns the resource and determines if it's visible based on the state of its dependencies.
func (s *stateTree) Get(key Ref) (res *Resource, visible bool, found bool) {
	idx, ok := s.byRef[key]
	if !ok {
		return nil, false, false
	}
	return idx.Resource, len(idx.PendingDependencies) == 0, true
}

// UpdateState updates the state of a resource and requeues dependents if necessary.
func (s *stateTree) UpdateState(ref Ref, state *apiv1.ResourceState, enqueue func(Ref)) {
	idx, ok := s.byRef[ref]
	if !ok {
		return
	}

	// Requeue self when the state has changed
	lastKnown := idx.State
	if lastKnown == nil || !lastKnown.Equal(state) {
		enqueue(ref)
	}

	// Dependents should no longer be blocked by this resource
	if state.Ready != nil && (lastKnown == nil || lastKnown.Ready == nil) {
		for _, dep := range idx.Dependents {
			delete(dep.PendingDependencies, ref)
			enqueue(dep.Resource.Ref)
		}
	}

	idx.State = state
}

// MarshalJSON allows the current tree to be serialized to JSON for testing/debugging purposes.
// This should not be expected to provide a stable schema.
func (s *stateTree) MarshalJSON() ([]byte, error) {
	tree := map[string]any{}
	for key, value := range s.byRef {
		dependencies := []string{}
		for ref := range value.PendingDependencies {
			dependencies = append(dependencies, ref.String())
		}
		slices.Sort(dependencies)

		dependents := []string{}
		for ref := range value.Dependents {
			dependents = append(dependents, ref.String())
		}
		slices.Sort(dependents)

		valMap := map[string]any{
			"ready":        value.State != nil && value.State.Ready != nil,
			"reconciled":   value.State != nil && value.State.Reconciled,
			"dependencies": dependencies,
			"dependents":   dependents,
		}
		tree[key.String()] = valMap
	}
	return json.Marshal(&tree)
}
