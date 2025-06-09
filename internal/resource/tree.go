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
	Seen                bool
	PendingDependencies map[Ref]struct{}
	Dependents          map[Ref]*indexedResource
	CompositionDeleting bool
}

// Backtracks returns true if visibility would cause the resource to backtrack to a previous state.
// This is possible if a resource also has a patch defined in a different readiness group.
// The earlier resource should not be visible to the reconciler once the later one has become visible,
// otherwise the reconciler would re-apply the previous state and oscillate between the two.
func (i *indexedResource) Backtracks() bool {
	for _, dep := range i.Dependents {
		matches := dep.Resource.GVK == i.Resource.GVK && dep.Resource.Ref.Name == i.Resource.Ref.Name && dep.Resource.Ref.Namespace == i.Resource.Ref.Namespace
		hasPendingDeps := len(dep.PendingDependencies) > 0
		if matches && !hasPendingDeps {
			return true
		}
	}
	return false
}

// treeBuilder is used to index a set of resources into a stateTree.
type treeBuilder struct {
	byRef        map[Ref]*indexedResource                    // fast key/value lookup by group/kind/ns/name
	byGroup      *redblacktree.Tree[int, []*indexedResource] // fast search for sparse readiness groups
	byDefiningGK map[schema.GroupKind]*indexedResource       // index CRDs by the GK they define
	byGK         map[schema.GroupKind]*indexedResource       // index all resources by their GK
}

func (b *treeBuilder) init() {
	if b.byRef == nil {
		b.byRef = map[Ref]*indexedResource{}
	}
	if b.byGroup == nil {
		b.byGroup = redblacktree.New[int, []*indexedResource]()
	}
	if b.byDefiningGK == nil {
		b.byDefiningGK = map[schema.GroupKind]*indexedResource{}
	}
	if b.byGK == nil {
		b.byGK = map[schema.GroupKind]*indexedResource{}
	}
}

func (b *treeBuilder) Add(resource *Resource) {
	b.init()

	// Handle conflicting refs deterministically
	if existing, ok := b.byRef[resource.Ref]; ok && resource.Less(existing.Resource) {
		return
	}

	// Index the resource into the builder
	idx := &indexedResource{
		Resource:            resource,
		PendingDependencies: map[Ref]struct{}{},
		Dependents:          map[Ref]*indexedResource{},
	}
	b.byRef[resource.Ref] = idx
	current, _ := b.byGroup.Get(resource.ReadinessGroup)
	b.byGroup.Put(resource.ReadinessGroup, append(current, idx))
	b.byGK[resource.GVK.GroupKind()] = idx
	if resource.DefinedGroupKind != nil {
		b.byDefiningGK[*resource.DefinedGroupKind] = idx
	}
}

func (b *treeBuilder) Build() *tree {
	t := &tree{
		byRef:     b.byRef,
		byManiRef: map[ManifestRef]*indexedResource{},
	}

	for _, idx := range b.byRef {
		t.byManiRef[idx.Resource.ManifestRef] = idx

		// CRs are dependent on their CRDs
		i := b.byGroup.IteratorAt(b.byGroup.GetNode(idx.Resource.ReadinessGroup))
		crd, ok := b.byDefiningGK[idx.Resource.GVK.GroupKind()]
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
		i.Next() // Prev always moves the cursor, even if it returns false

		// Any resources in the next readiness group depend on us
		if i.Next() && i.Key() > idx.Resource.ReadinessGroup {
			for _, cur := range i.Value() {
				idx.Dependents[cur.Resource.Ref] = cur
			}
		}
	}

	return t
}

// tree is an optimized, indexed representation of a set of resources.
// NOT CONCURRENCY SAFE.
type tree struct {
	byRef     map[Ref]*indexedResource
	byManiRef map[ManifestRef]*indexedResource
}

// Get returns the resource and determines if it's visible based on the state of its dependencies.
func (t *tree) Get(key Ref) (res *Resource, visible bool, found bool) {
	idx, ok := t.byRef[key]
	if !ok {
		return nil, false, false
	}
	return idx.Resource, (!idx.Backtracks() && len(idx.PendingDependencies) == 0) || idx.CompositionDeleting, true
}

// UpdateState updates the state of a resource and requeues dependents if necessary.
func (t *tree) UpdateState(comp *apiv1.Composition, ref ManifestRef, state *apiv1.ResourceState, enqueue func(Ref)) {
	idx, ok := t.byManiRef[ref]
	if !ok {
		return
	}

	// Requeue self when the state has changed
	lastKnown := idx.Resource.latestKnownState.Swap(state)
	if (!idx.Seen && lastKnown == nil) || !lastKnown.Equal(state) || (!idx.CompositionDeleting && comp.DeletionTimestamp != nil) {
		enqueue(idx.Resource.Ref)
	}
	idx.Seen = true
	idx.CompositionDeleting = comp.DeletionTimestamp != nil

	// Dependents should no longer be blocked by this resource
	if state.Ready != nil && (lastKnown == nil || lastKnown.Ready == nil) {
		for _, dep := range idx.Dependents {
			delete(dep.PendingDependencies, idx.Resource.Ref)
			enqueue(dep.Resource.Ref)
		}
	}
}

// MarshalJSON allows the current tree to be serialized to JSON for testing/debugging purposes.
// This should not be expected to provide a stable schema.
func (t *tree) MarshalJSON() ([]byte, error) {
	tree := map[string]any{}
	for key, value := range t.byRef {
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

		state := value.Resource.latestKnownState.Load()
		valMap := map[string]any{
			"ready":        state != nil && state.Ready != nil,
			"reconciled":   state != nil && state.Reconciled,
			"dependencies": dependencies,
			"dependents":   dependents,
		}
		tree[key.String()] = valMap
	}
	return json.Marshal(&tree)
}
