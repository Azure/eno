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
	resources              map[synthesisKey]map[resourceKey]*Resource
	synthesesByComposition map[types.NamespacedName][]int64
}

func newCache(client client.Client) *cache {
	return &cache{
		client:                 client,
		resources:              make(map[synthesisKey]map[resourceKey]*Resource),
		synthesesByComposition: make(map[types.NamespacedName][]int64),
	}
}

func (c *cache) Get(ctx context.Context, comp *apiv1.Composition, ref *ResourceRef, gen int64) (*Resource, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	synKey := synthesisKey{Composition: compNSN, Generation: gen, Deleting: comp.DeletionTimestamp != nil}
	resources, ok := c.resources[synKey]
	if !ok {
		return nil, false
	}

	resKey := resourceKey{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
	res, ok := resources[resKey]
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
	key := synthesisKey{
		Composition: types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace},
		Generation:  synthesis.ObservedCompositionGeneration,
		Deleting:    comp.DeletionTimestamp != nil,
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
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

	// Building resources can be expensive (json parsing, etc.) so don't hold the lock during this call
	resources, requests, err := c.buildResources(ctx, comp, items)
	if err != nil {
		return nil, err
	}

	c.mut.Lock()
	defer c.mut.Unlock()

	synKey := synthesisKey{Composition: compNSN, Generation: synthesis.ObservedCompositionGeneration, Deleting: comp.DeletionTimestamp != nil}
	c.resources[synKey] = resources
	c.synthesesByComposition[compNSN] = append(c.synthesesByComposition[compNSN], synKey.Generation)

	logger.Info("cache filled")
	return requests, nil
}

func (c *cache) buildResources(ctx context.Context, comp *apiv1.Composition, items []apiv1.ResourceSlice) (map[resourceKey]*Resource, []*Request, error) {
	resources := map[resourceKey]*Resource{}
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
			key := resourceKey{Kind: res.Ref.Kind, Namespace: res.Ref.Namespace, Name: res.Ref.Name}
			resources[key] = res
			requests = append(requests, &Request{
				ResourceRef: *res.Ref,
				Manifest: ManifestRef{
					Slice: types.NamespacedName{
						Namespace: slice.Namespace,
						Name:      slice.Name,
					},
					Index: i,
				},
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

	compRef := &CompositionRef{
		Name:      comp.Name,
		Namespace: comp.Namespace,
	}
	if comp.Status.CurrentState != nil {
		compRef.Generation = comp.Status.CurrentState.ObservedCompositionGeneration
	}

	res := &Resource{
		Ref: &ResourceRef{
			Composition: compRef,
			Namespace:   parsed.GetNamespace(),
			Name:        parsed.GetName(),
			Kind:        parsed.GetKind(),
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
		if comp != nil && (comp.DeletionTimestamp != nil || (comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedCompositionGeneration == syn) || (comp.Status.PreviousState != nil && comp.Status.PreviousState.ObservedCompositionGeneration == syn)) {
			remainingSyns = append(remainingSyns, syn)
			continue // still referenced by the Generation
		}
		key := synthesisKey{
			Composition: compNSN,
			Generation:  syn,
			Deleting:    comp != nil && comp.DeletionTimestamp != nil,
		}
		delete(c.resources, key)

		// If there aren't any syntheses of this composition remaining,
		// we need to remove any deleting OR non-deleting syntheses since
		// we have no way of knowing the last state of the composition i.e.
		// was it being deleted.
		if comp == nil {
			key.Deleting = !key.Deleting
			delete(c.resources, key)
		}
	}
	c.synthesesByComposition[compNSN] = remainingSyns
}

type synthesisKey struct {
	Composition types.NamespacedName
	Generation  int64
	Deleting    bool
}

type resourceKey struct {
	Kind, Namespace, Name string
}
