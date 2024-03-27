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
	"github.com/go-logr/logr"
)

// cache maintains a fast index of (ResourceRef + Composition + Synthesis) -> Resource.
type cache struct {
	client client.Client
	renv   *readiness.Env

	mut                    sync.Mutex
	resources              map[CompositionRef]map[resource.Ref]*Resource
	synthesesByComposition map[types.NamespacedName][]int64
}

func newCache(client client.Client) *cache {
	renv, err := readiness.NewEnv()
	if err != nil {
		panic(fmt.Sprintf("error setting up readiness expression env: %s", err))
	}
	return &cache{
		client:                 client,
		renv:                   renv,
		resources:              make(map[CompositionRef]map[resource.Ref]*Resource),
		synthesesByComposition: make(map[types.NamespacedName][]int64),
	}
}

func (c *cache) Get(ctx context.Context, comp *CompositionRef, ref *resource.Ref) (*Resource, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	resources, ok := c.resources[*comp]
	if !ok {
		return nil, false
	}

	res, ok := resources[*ref]
	if !ok {
		return nil, false
	}

	return res, ok
}

// HasSynthesis returns true when the cache contains the resulting resources of the given synthesis.
// This should be called before Fill to determine if filling is necessary.
func (c *cache) HasSynthesis(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) bool {
	key := CompositionRef{
		Name:       comp.Name,
		Namespace:  comp.Namespace,
		Generation: synthesis.ObservedCompositionGeneration,
	}

	c.mut.Lock()
	_, exists := c.resources[key]
	c.mut.Unlock()
	return exists
}

// Fill populates the cache with all (or no) resources that are part of the given synthesis.
// Requests to be enqueued are returned. Although this arguably violates separation of concerns, it's convenient and efficient.
func (c *cache) Fill(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis, items []apiv1.ResourceSlice) ([]*Request, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Building resources can be expensive (json parsing, etc.) so don't hold the lock during this call
	resources, requests, err := c.buildResources(ctx, comp, items)
	if err != nil {
		return nil, err
	}

	c.mut.Lock()
	defer c.mut.Unlock()

	synKey := CompositionRef{Name: comp.Name, Namespace: comp.Namespace, Generation: synthesis.ObservedCompositionGeneration}
	c.resources[synKey] = resources

	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	c.synthesesByComposition[compNSN] = append(c.synthesesByComposition[compNSN], synKey.Generation)

	logger.V(0).Info("cache filled")
	return requests, nil
}

func (c *cache) buildResources(ctx context.Context, comp *apiv1.Composition, items []apiv1.ResourceSlice) (map[resource.Ref]*Resource, []*Request, error) {
	resources := map[resource.Ref]*Resource{}
	requests := []*Request{}
	for _, slice := range items {
		slice := slice
		if slice.DeletionTimestamp == nil && comp.DeletionTimestamp != nil {
			return nil, nil, errors.New("stale informer - refusing to fill cache")
		}

		// NOTE: In the future we can build a DAG here to find edges between dependant resources and append them to the Resource structs

		for i, obj := range slice.Spec.Resources {
			obj := obj

			res, err := resource.New(ctx, c.renv, &slice, &obj)
			if err != nil {
				return nil, nil, fmt.Errorf("building resource at index %d of slice %s: %w", i, slice.Name, err)
			}
			resources[res.Ref] = res
			requests = append(requests, &Request{
				Resource: res.Ref,
				Manifest: ManifestRef{
					Slice: types.NamespacedName{
						Namespace: slice.Namespace,
						Name:      slice.Name,
					},
					Index: i,
				},
				Composition: types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace},
			})
		}
	}

	return resources, requests, nil
}

// Purge removes resources associated with a particular composition synthesis from the cache.
// If composition is set, resources from the active syntheses will be retained.
// Otherwise all resources deriving from the referenced composition are removed.
// This design allows the cache to stay consistent without deletion tombstones.
func (c *cache) Purge(ctx context.Context, compNSN types.NamespacedName, comp *apiv1.Composition) {
	c.mut.Lock()
	defer c.mut.Unlock()

	remainingSyns := []int64{}
	for _, syn := range c.synthesesByComposition[compNSN] {
		// Don't touch any syntheses still referenced by the composition
		if comp != nil && ((comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.ObservedCompositionGeneration == syn) || (comp.Status.PreviousSynthesis != nil && comp.Status.PreviousSynthesis.ObservedCompositionGeneration == syn)) {
			remainingSyns = append(remainingSyns, syn)
			continue // still referenced by the Generation
		}
		delete(c.resources, CompositionRef{
			Name:       compNSN.Name,
			Namespace:  compNSN.Namespace,
			Generation: syn,
		})
	}
	c.synthesesByComposition[compNSN] = remainingSyns
}
