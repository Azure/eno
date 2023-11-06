package reconstitution

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

// cache maintains a fast index of (ResourceRef + generation) -> Resource.
type cache struct {
	client client.Client

	mut                    sync.Mutex
	resources              map[resourceKey]*Resource
	synthesesByComposition map[types.NamespacedName][]int64
	resourcesBySynthesis   map[synthesisKey][]resourceKey
}

func newCache(client client.Client) *cache {
	return &cache{
		client:                 client,
		resources:              make(map[resourceKey]*Resource),
		synthesesByComposition: make(map[types.NamespacedName][]int64),
		resourcesBySynthesis:   make(map[synthesisKey][]resourceKey),
	}
}

func (c *cache) Get(gen int64, ref *ResourceRef) (*Resource, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	res, ok := c.resources[resourceKey{
		ResourceRef:           *ref,
		CompositionGeneration: gen,
	}]
	return res, ok
}

// HasSynthesis returns true if the cache contains the resources generated as part of the given synthesis.
func (c *cache) HasSynthesis(comp types.NamespacedName, synthesis *apiv1.Synthesis) bool {
	key := synthesisKey{
		Composition: comp,
		Generation:  synthesis.ObservedGeneration,
	}

	c.mut.Lock()
	_, exists := c.resourcesBySynthesis[key]
	c.mut.Unlock()
	return exists
}

// Fill populates the cache with resources from the given slices and associates them with a
// composition and synthesis such that they can easily be purged from the cache after deletion.
func (c *cache) Fill(ctx context.Context, comp types.NamespacedName, synthesis *apiv1.Synthesis, items []apiv1.ResourceSlice) ([]*Request, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Building resources can be expensive (secret lookups, json parsing, etc.) so don't hold the lock during this call
	resources, requests, err := c.buildResources(ctx, comp, items)
	if err != nil {
		return nil, err
	}

	c.mut.Lock()
	defer c.mut.Unlock()

	synKey := synthesisKey{Composition: comp, Generation: synthesis.ObservedGeneration}

	// Cache the resources by their lookup key
	resKeys := []resourceKey{}
	for rk, resource := range resources {
		resKeys = append(resKeys, rk)
		c.resources[rk] = resource
	}

	// Associate each resource with this synthesis
	c.resourcesBySynthesis[synKey] = resKeys

	// Associate this synthesis with the composition
	c.synthesesByComposition[comp] = append(c.synthesesByComposition[comp], synthesis.ObservedGeneration)

	logger.Info("cache filled")
	return requests, nil
}

func (c *cache) buildResources(ctx context.Context, comp types.NamespacedName, items []apiv1.ResourceSlice) (map[resourceKey]*Resource, []*Request, error) {
	resources := map[resourceKey]*Resource{}
	requests := []*Request{}
	for _, slice := range items {
		slice := slice

		// NOTE: In the future we can build a DAG here to find edges between dependant resources

		for i, resource := range slice.Spec.Resources {
			resource := resource
			gr, err := c.buildResource(ctx, &slice, &resource)
			if err != nil {
				return nil, nil, fmt.Errorf("building resource at index %d of slice %s: %w", i, slice.Name, err)
			}
			key := resourceKey{
				ResourceRef:           *gr.Ref,
				CompositionGeneration: slice.Spec.CompositionGeneration,
			}
			resources[key] = gr
			requests = append(requests, &Request{
				ResourceRef: *gr.Ref,
				Composition: types.NamespacedName{
					Namespace: comp.Namespace,
					Name:      comp.Name,
				},
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

func (c *cache) buildResource(ctx context.Context, slice *apiv1.ResourceSlice, resource *apiv1.Manifest) (*Resource, error) {
	manifest := resource.Manifest
	if resource.SecretName != nil {
		secret := &corev1.Secret{}
		secret.Name = *resource.SecretName
		secret.Namespace = slice.Namespace
		err := c.client.Get(ctx, client.ObjectKeyFromObject(secret), secret)
		if err != nil {
			return nil, fmt.Errorf("getting secret: %w", err)
		}
		if secret.Data != nil {
			manifest = string(secret.Data["manifest"])
		}
	}

	parsed := &unstructured.Unstructured{}
	err := parsed.UnmarshalJSON([]byte(manifest))
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	gr := &Resource{
		Ref: &ResourceRef{
			Namespace: parsed.GetNamespace(),
			Name:      parsed.GetName(),
			Kind:      parsed.GetKind(),
		},
		Manifest: manifest,
		Object:   parsed,
	}
	if resource.ReconcileInterval != nil {
		gr.ReconcileInterval = resource.ReconcileInterval.Duration
	}
	if gr.Ref.Name == "" || gr.Ref.Kind == "" || parsed.GetAPIVersion() == "" {
		return nil, fmt.Errorf("missing name, kind, or apiVersion")
	}
	return gr, nil
}

// Purge removes resources associated with a particular composition synthesis from the cache.
// If composition is set, resources from the active syntheses will be retained.
// Otherwise all resources deriving from the referenced composition are removed.
// This design allows the cache to stay consistent without deletion tombstones.
func (c *cache) Purge(ctx context.Context, compNSN types.NamespacedName, comp *apiv1.Composition) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	synGens := c.synthesesByComposition[compNSN]
	newGens := []int64{}
	for _, gen := range synGens {
		// If the composition still exists, don't remove syntheses that are referenced by it
		if comp != nil && ((comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedGeneration == gen) || (comp.Status.PreviousState != nil && comp.Status.PreviousState.ObservedGeneration == gen)) {
			newGens = append(newGens, gen)
			continue // still referenced by the Generation
		}

		synKey := synthesisKey{Composition: compNSN, Generation: gen}

		// Remove resources from the main lookup map
		for _, key := range c.resourcesBySynthesis[synKey] {
			delete(c.resources, key)
		}

		// Remove mapping of synthesis -> resource keys
		delete(c.resourcesBySynthesis, synKey)
		logger.V(1).Info("purged synthesis from cache", "synthesisGen", gen)
	}

	// Don't orphan composition -> synthesis mappings
	if len(newGens) == 0 {
		delete(c.synthesesByComposition, compNSN)
		logger.V(1).Info("no more synthesis exist for this composition - removing from cache")
	} else {
		c.synthesesByComposition[compNSN] = newGens
	}
}

type resourceKey struct {
	ResourceRef
	CompositionGeneration int64
}

type synthesisKey struct {
	Composition types.NamespacedName
	Generation  int64
}
