package resource

import (
	"context"
	"sync"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

type Request struct {
	Resource    Ref
	Composition types.NamespacedName
}

// Cache caches resources indexed and logically grouped by the UUID of the synthesis that produced them.
// Kind of like an informer but optimized for Eno.
type Cache struct {
	renv  *readiness.Env
	queue workqueue.TypedRateLimitingInterface[Request]

	mut       sync.Mutex
	syntheses map[string]*tree
	synByComp map[types.NamespacedName][]string
}

func NewCache(renv *readiness.Env, queue workqueue.TypedRateLimitingInterface[Request]) *Cache {
	return &Cache{
		renv:      renv,
		queue:     queue,
		syntheses: map[string]*tree{},
		synByComp: map[types.NamespacedName][]string{},
	}
}

func (c *Cache) Get(ctx context.Context, synthesisUUID string, ref Ref) (res *Resource, visible, found bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	syn, ok := c.syntheses[synthesisUUID]
	if !ok {
		return nil, false, false
	}
	return syn.Get(ref)
}

// Visit takes a set of resource slices from the informers and updates the resource status in the cache.
// Return false if the synthesis is not in the cache.
func (c *Cache) Visit(ctx context.Context, comp *apiv1.Composition, synUUID string, items []apiv1.ResourceSlice) bool {
	c.mut.Lock()
	defer c.mut.Unlock()

	syn, ok := c.syntheses[synUUID]
	if !ok {
		return false
	}

	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	for _, slice := range items {
		for i := 0; i < len(slice.Spec.Resources); i++ {
			var state apiv1.ResourceState
			if len(slice.Status.Resources) > i {
				state = slice.Status.Resources[i]
			}
			ref := ManifestRef{
				Slice: types.NamespacedName{Name: slice.Name, Namespace: slice.Namespace},
				Index: i,
			}
			syn.UpdateState(comp, ref, &state, func(r Ref) {
				c.queue.Add(Request{Resource: r, Composition: compNSN})
			})
		}
	}

	return true
}

// Fill populates the cache with resources from a synthesis. Call Visit first to see if filling the cache is necessary.
// Get the resource slices from the API - not the informers, which prune out the manifests to save memory.
func (c *Cache) Fill(ctx context.Context, comp types.NamespacedName, synUUID string, items []apiv1.ResourceSlice) {
	logger := logr.FromContextOrDiscard(ctx)

	var builder treeBuilder
	for _, slice := range items {
		slice := slice
		for i := range slice.Spec.Resources {
			res, err := NewResource(ctx, c.renv, &slice, i)
			if err != nil {
				// This should be impossible since the synthesis executor process will not produce invalid resources
				logger.Error(err, "invalid resource - cannot load into cache", "resourceSliceName", slice.Name, "resourceIndex", i)
				return
			}
			builder.Add(res)
		}
	}
	tree := builder.Build()

	c.mut.Lock()
	c.syntheses[synUUID] = tree
	c.synByComp[comp] = append(c.synByComp[comp], synUUID)
	c.mut.Unlock()
	logger.V(1).Info("resource cache filled", "synthesisUUID", synUUID)
}

// Purge removes all syntheses from the cache that are not part of the given composition.
// If comp is nil, all syntheses will be purged.
func (c *Cache) Purge(ctx context.Context, compNSN types.NamespacedName, comp *apiv1.Composition) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	remainingSyns := []string{}
	for _, uuid := range c.synByComp[compNSN] {
		if comp != nil && ((comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID == uuid) || (comp.Status.PreviousSynthesis != nil && comp.Status.PreviousSynthesis.UUID == uuid)) {
			remainingSyns = append(remainingSyns, uuid)
			continue // still referenced
		}

		logger.V(1).Info("resource cache purged", "synthesisUUID", uuid)
		delete(c.syntheses, uuid)
	}

	c.synByComp[compNSN] = remainingSyns
}
