package reconstitution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	"github.com/Azure/eno/internal/resource"
	"github.com/emirpasic/gods/v2/trees/redblacktree"
	"github.com/go-logr/logr"
)

// TODO: Log synthesis UUID everywhere

// Cache maintains a fast index of (ResourceRef + Composition + Synthesis) -> Resource.
type Cache struct {
	client client.Client
	renv   *readiness.Env

	mut                         sync.Mutex
	resources                   map[SynthesisRef]*resources
	synthesisUUIDsByComposition map[types.NamespacedName][]string
	byIndex                     map[sliceIndex]*Resource
}

// resources contains a set of indexed resources scoped to a single Composition
type resources struct {
	ByRef            map[resource.Ref]*Resource
	ByReadinessGroup *redblacktree.Tree[uint, []*Resource]
}

type sliceIndex struct {
	Index     int
	SliceName string
	Namespace string
}

func NewCache(client client.Client) *Cache {
	renv, err := readiness.NewEnv()
	if err != nil {
		panic(fmt.Sprintf("error setting up readiness expression env: %s", err))
	}
	return &Cache{
		client:                      client,
		renv:                        renv,
		resources:                   make(map[SynthesisRef]*resources),
		synthesisUUIDsByComposition: make(map[types.NamespacedName][]string),
		byIndex:                     make(map[sliceIndex]*resource.Resource),
	}
}

func (c *Cache) Get(ctx context.Context, comp *SynthesisRef, ref *resource.Ref) (*Resource, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	resources, ok := c.resources[*comp]
	if !ok {
		return nil, false
	}

	res, ok := resources.ByRef[*ref]
	if !ok {
		return nil, false
	}

	return res, ok
}

func (c *Cache) RangeByReadinessGroup(ctx context.Context, comp *SynthesisRef, group uint, dir int) []*Resource {
	c.mut.Lock()
	defer c.mut.Unlock()

	if group == 0 && dir == -1 {
		return nil
	}

	resources, ok := c.resources[*comp]
	if !ok {
		return nil
	}

	node := resources.ByReadinessGroup.GetNode(group)
	if node == nil {
		return nil // the given group must have a resource, otherwise we wouldn't be looking it up
	}

	// If we're adjacent...
	if dir > 0 {
		if node.Right != nil {
			return node.Right.Value
		}
	} else {
		if node.Left != nil {
			return node.Left.Value
		}
	}

	// ...otherwise we need to find it
	if dir > 0 {
		node, ok = resources.ByReadinessGroup.Ceiling(group + 1)
	} else {
		node, ok = resources.ByReadinessGroup.Floor(group - 1)
	}
	if !ok {
		return nil // no previous node!
	}

	return node.Value
}

func (c *Cache) getByIndex(ctx context.Context, idx *sliceIndex) (*Resource, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	res, ok := c.byIndex[*idx]
	if !ok {
		return nil, false
	}

	return res, ok
}

// hasSynthesis returns true when the cache contains the resulting resources of the given synthesis.
// This should be called before Fill to determine if filling is necessary.
func (c *Cache) hasSynthesis(comp *apiv1.Composition, synthesis *apiv1.Synthesis) bool {
	key := SynthesisRef{
		CompositionName: comp.Name,
		Namespace:       comp.Namespace,
		UUID:            synthesis.UUID,
	}

	c.mut.Lock()
	_, exists := c.resources[key]
	c.mut.Unlock()
	return exists
}

// fill populates the cache with all (or no) resources that are part of the given synthesis.
// Requests to be enqueued are returned. Although this arguably violates separation of concerns, it's convenient and efficient.
func (c *Cache) fill(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis, items []apiv1.ResourceSlice) ([]*Request, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Building resources can be expensive (json parsing, etc.) so don't hold the lock during this call
	resources, requests, err := c.buildResources(ctx, comp, items)
	if err != nil {
		return nil, err
	}

	c.mut.Lock()
	defer c.mut.Unlock()

	synKey := SynthesisRef{CompositionName: comp.Name, Namespace: comp.Namespace, UUID: synthesis.UUID}
	c.resources[synKey] = resources

	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	c.synthesisUUIDsByComposition[compNSN] = append(c.synthesisUUIDsByComposition[compNSN], synKey.UUID)

	logger.V(0).Info("cache filled")
	return requests, nil
}

func (c *Cache) buildResources(ctx context.Context, comp *apiv1.Composition, items []apiv1.ResourceSlice) (*resources, []*Request, error) {
	resources := &resources{
		ByRef:            map[resource.Ref]*Resource{},
		ByReadinessGroup: redblacktree.New[uint, []*Resource](),
	}
	requests := []*Request{}
	for _, slice := range items {
		slice := slice
		if slice.DeletionTimestamp == nil && comp.DeletionTimestamp != nil {
			return nil, nil, errors.New("stale informer - refusing to fill cache")
		}

		for i := range slice.Spec.Resources {
			res, err := resource.NewResource(ctx, c.renv, &slice, i)
			if err != nil {
				return nil, nil, fmt.Errorf("building resource at index %d of slice %s: %w", i, slice.Name, err)
			}
			resources.ByRef[res.Ref] = res
			c.byIndex[sliceIndex{Index: i, SliceName: slice.Name, Namespace: slice.Namespace}] = res

			current, _ := resources.ByReadinessGroup.Get(res.ReadinessGroup)
			resources.ByReadinessGroup.Put(res.ReadinessGroup, append(current, res))

			requests = append(requests, &Request{
				Resource:    res.Ref,
				Composition: types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace},
			})
		}
	}

	return resources, requests, nil
}

// purge removes resources associated with a particular composition synthesis from the cache.
// If composition is set, resources from the active syntheses will be retained.
// Otherwise all resources deriving from the referenced composition are removed.
// This design allows the cache to stay consistent without deletion tombstones.
func (c *Cache) purge(compNSN types.NamespacedName, comp *apiv1.Composition) {
	c.mut.Lock()
	defer c.mut.Unlock()

	remainingSyns := []string{}
	for _, uuid := range c.synthesisUUIDsByComposition[compNSN] {
		// Don't touch any syntheses still referenced by the composition
		if comp != nil && ((comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID == uuid) || (comp.Status.PreviousSynthesis != nil && comp.Status.PreviousSynthesis.UUID == uuid)) {
			remainingSyns = append(remainingSyns, uuid)
			continue // still referenced
		}
		delete(c.resources, SynthesisRef{
			CompositionName: compNSN.Name,
			Namespace:       compNSN.Namespace,
			UUID:            uuid,
		})
	}
	c.synthesisUUIDsByComposition[compNSN] = remainingSyns
}
