package reconstitution

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

// cache maintains a fast index of (ResourceRef + Composition + Synthesis) -> Resource.
type cache struct {
	client client.Client

	mut                    sync.Mutex
	resources              map[CompositionRef]map[ResourceRef]*Resource
	synthesesByComposition map[types.NamespacedName][]int64
}

func newCache(client client.Client) *cache {
	return &cache{
		client:                 client,
		resources:              make(map[CompositionRef]map[ResourceRef]*Resource),
		synthesesByComposition: make(map[types.NamespacedName][]int64),
	}
}

func (c *cache) Get(ctx context.Context, comp *CompositionRef, ref *ResourceRef) (*Resource, bool) {
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

	// Copy the resource so it's safe for callers to mutate
	refDeref := *ref
	return &Resource{
		Ref:      &refDeref,
		Manifest: res.Manifest.DeepCopy(),
		Object:   res.Object.DeepCopy(),
	}, ok
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

	logger.Info("cache filled")
	return requests, nil
}

func (c *cache) buildResources(ctx context.Context, comp *apiv1.Composition, items []apiv1.ResourceSlice) (map[ResourceRef]*Resource, []*Request, error) {
	resources := map[ResourceRef]*Resource{}
	requests := []*Request{}
	for _, slice := range items {
		slice := slice

		// NOTE: In the future we can build a DAG here to find edges between dependant resources and append them to the Resource structs

		for i, resource := range slice.Spec.Resources {
			resource := resource

			// Delete the resource if the composition is being deleted
			if comp.DeletionTimestamp != nil {
				resource.Deleted = true
			}

			res, err := c.buildResource(ctx, comp, &slice, &resource)
			if err != nil {
				return nil, nil, fmt.Errorf("building resource at index %d of slice %s: %w", i, slice.Name, err)
			}
			resources[*res.Ref] = res
			requests = append(requests, &Request{
				Resource: *res.Ref,
				Manifest: ManifestRef{
					Slice: types.NamespacedName{
						Namespace: slice.Namespace,
						Name:      slice.Name,
					},
					Index: i,
				},
				Composition: *NewCompositionRef(comp),
			})
		}
	}

	return resources, requests, nil
}

func (c *cache) buildResource(ctx context.Context, comp *apiv1.Composition, slice *apiv1.ResourceSlice, resource *apiv1.Manifest) (*Resource, error) {
	manifest := resource.Manifest
	parsed := &unstructured.Unstructured{}
	err := parsed.UnmarshalJSON([]byte(manifest))
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	res := &Resource{
		Ref: &ResourceRef{
			Namespace: parsed.GetNamespace(),
			Name:      parsed.GetName(),
			Kind:      parsed.GetKind(),
		},
		Manifest: resource,
		Object:   parsed,
	}
	if res.Ref.Name == "" || parsed.GetAPIVersion() == "" {
		return nil, fmt.Errorf("missing name, kind, or apiVersion")
	}
	return res, nil
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
		if comp != nil && ((comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedCompositionGeneration == syn) || (comp.Status.PreviousState != nil && comp.Status.PreviousState.ObservedCompositionGeneration == syn)) {
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
