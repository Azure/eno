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
	Seen                bool
	PendingDependencies map[Ref]struct{}
	Dependents          map[Ref]*indexedResource
}

// treeBuilder is used to index a set of resources into a stateTree.
type treeBuilder struct {
	byRef        map[Ref]*indexedResource
	byGroup      *redblacktree.Tree[int, []*indexedResource]
	byDefiningGK map[schema.GroupKind]*indexedResource
	byGK         map[schema.GroupKind]*indexedResource
}

func (b *treeBuilder) Add(resource *Resource) {
	// Initialize the builder
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
	return idx.Resource, len(idx.PendingDependencies) == 0, true
}

// UpdateState updates the state of a resource and requeues dependents if necessary.
func (t *tree) UpdateState(ref ManifestRef, state *apiv1.ResourceState, enqueue func(Ref)) {
	idx, ok := t.byManiRef[ref]
	if !ok {
		return
	}

	// Requeue self when the state has changed
	lastKnown := idx.State
	if (!idx.Seen && lastKnown == nil) || !lastKnown.Equal(state) {
		enqueue(idx.Resource.Ref)
	}

	// Dependents should no longer be blocked by this resource
	if state.Ready != nil && (lastKnown == nil || lastKnown.Ready == nil) {
		for _, dep := range idx.Dependents {
			delete(dep.PendingDependencies, idx.Resource.Ref)
			enqueue(dep.Resource.Ref)
		}
	}

	idx.State = state
	idx.Seen = true
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
