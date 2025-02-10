package resource

import (
	"context"
	"math"
	"sync"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

// TODO: Better log levels

type Request struct {
	Resource    Ref
	Composition types.NamespacedName
}

type Cache struct {
	renv  *readiness.Env
	Queue workqueue.TypedRateLimitingInterface[Request]

	mut       sync.Mutex
	syntheses map[string]*synthesis
	synUUIDs  map[types.NamespacedName][]string // by composition
}

func NewCache(renv *readiness.Env, queue workqueue.TypedRateLimitingInterface[Request]) *Cache {
	return &Cache{
		renv:      renv,
		Queue:     queue,
		syntheses: map[string]*synthesis{},
		synUUIDs:  map[types.NamespacedName][]string{},
	}
}

func (c *Cache) Get(synthesisUUID string, ref *Ref) (*Resource, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()

	syn, ok := c.syntheses[synthesisUUID]
	if !ok {
		return nil, false
	}

	res, ok := syn.byRef[*ref]
	return res, ok
}

func (c *Cache) Visible(synthesisUUID string, ref *Ref) bool {
	c.mut.Lock()
	defer c.mut.Unlock()

	syn, ok := c.syntheses[synthesisUUID]
	if !ok {
		// Always fail open in unexpected cases to avoid blocking the loop
		return true
	}
	res, ok := syn.byRef[*ref]
	if !ok {
		return true
	}

	// CRs implicitly depend on the defining CRD
	if res.lastState == nil || !res.lastState.Reconciled {
		if crd, ok := syn.byDefinedGroupKind[res.GVK.GroupKind()]; ok {
			if crd.lastState == nil || !crd.lastState.Reconciled {
				return false
			}
		}
	}

	return syn.readinessCursor >= res.ReadinessGroup
}

// Visit processes resource status transitions and returns true if the synthesis is known by the cache.
func (c *Cache) Visit(ctx context.Context, comp *apiv1.Composition, synUUID string, items []apiv1.ResourceSlice) bool {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	snap, ok := c.syntheses[synUUID]
	if !ok {
		return false
	}

	// Visit the state of every resource
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}
	firstGroup := math.MaxInt
	readinessGroups := map[int]bool{}
	for _, slice := range items {
		for i := 0; i < len(slice.Spec.Resources); i++ {
			res, ok := snap.byIndex[sliceIndex{Index: i, SliceName: slice.Name}]
			if !ok {
				continue
			}
			firstGroup = min(firstGroup, res.ReadinessGroup)

			var state apiv1.ResourceState
			if len(slice.Status.Resources) > i {
				state = slice.Status.Resources[i]
			}

			if state.Ready == nil {
				readinessGroups[res.ReadinessGroup] = false
			} else {
				current, ok := readinessGroups[res.ReadinessGroup]
				readinessGroups[res.ReadinessGroup] = !ok || current
			}

			if res.VisitState(&state) {
				c.Queue.Add(Request{Resource: res.Ref, Composition: compNSN})

				if res.DefinedGroupKind != nil {
					for _, child := range snap.byGroupKind[*res.DefinedGroupKind] {
						c.Queue.Add(Request{Resource: child.Ref, Composition: compNSN})
					}
				}
			}
		}
	}

	// The cursor is defined as the index of the readiness group at which all resources are visible.
	// Its value is inclusive e.g. resources >= the cursor can safely be reconciled.
	cursor := -math.MaxInt
	if firstGroup > cursor {
		cursor = firstGroup // the first known group is always visible
	}
	for grp, ready := range readinessGroups {
		if ready && grp > cursor {
			cursor = grp
		}
	}

	// Find the new readiness cursor
	oldCursor := snap.readinessCursor
	snap.readinessCursor = cursor
	if cursor > oldCursor {
		logger.V(1).Info("readiness cursor advanced", "synthesisUUID", synUUID, "oldCursor", oldCursor, "newCursor", cursor)
	} else {
		return true
	}

	// Enqueue any readiness groups that are now visible/ready
	//
	// Note that we could index the resources by readiness group but it isn't worth the memory since we only search for
	// resources between the old/current cursor when it moves.
	for _, slice := range items {
		for i := 0; i < len(slice.Spec.Resources); i++ {
			res, ok := snap.byIndex[sliceIndex{Index: i, SliceName: slice.Name}]
			if !ok {
				continue
			}

			var state apiv1.ResourceState
			if len(slice.Status.Resources) > i {
				state = slice.Status.Resources[i]
			}

			if !state.Reconciled && cursor > res.ReadinessGroup && oldCursor <= res.ReadinessGroup {
				c.Queue.Add(Request{Resource: res.Ref, Composition: compNSN})
			}
		}
	}

	return true
}

// Fill fills the cache and workqueue. Call Visit first and only call Fill if it returns false.
func (c *Cache) Fill(ctx context.Context, comp *apiv1.Composition, synUUID string, items []apiv1.ResourceSlice) {
	logger := logr.FromContextOrDiscard(ctx)
	compNSN := types.NamespacedName{Name: comp.Name, Namespace: comp.Namespace}

	// Parsing resources is relatively slow, don't take the lock yet
	snap := &synthesis{
		byRef:              map[Ref]*Resource{},
		byIndex:            map[sliceIndex]*Resource{},
		byGroupKind:        map[schema.GroupKind][]*Resource{},
		byDefinedGroupKind: map[schema.GroupKind]*Resource{},
		readinessCursor:    -math.MaxInt,
	}
	for _, slice := range items {
		slice := slice
		for i := range slice.Spec.Resources {
			res, err := NewResource(ctx, c.renv, &slice, i)
			if err != nil {
				// This should be impossible since the synthesis executor process will not produce invalid resources
				logger.Error(err, "invalid resource - cannot load into cache", "resourceSliceName", slice.Name, "resourceIndex", i)
				return
			}

			snap.byRef[res.Ref] = res
			snap.byIndex[sliceIndex{Index: i, SliceName: slice.Name}] = res
			snap.byGroupKind[res.GVK.GroupKind()] = append(snap.byGroupKind[res.GVK.GroupKind()], res)
			if dgk := res.DefinedGroupKind; dgk != nil {
				snap.byDefinedGroupKind[*dgk] = res
			}
		}
	}

	c.mut.Lock()
	c.syntheses[synUUID] = snap
	c.synUUIDs[compNSN] = append(c.synUUIDs[compNSN], synUUID)
	c.mut.Unlock()
	logger.V(1).Info("resource cache filled", "synthesisUUID", synUUID)
}

// GC "garbage collects" all cached syntheses for the composition referenced by compNSN.
// If comp is not nil, syntheses still referenced by the composition will be preserved.
func (c *Cache) GC(ctx context.Context, compNSN types.NamespacedName, comp *apiv1.Composition) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	remainingSyns := []string{}
	for _, uuid := range c.synUUIDs[compNSN] {
		if comp != nil && ((comp.Status.CurrentSynthesis != nil && comp.Status.CurrentSynthesis.UUID == uuid) || (comp.Status.PreviousSynthesis != nil && comp.Status.PreviousSynthesis.UUID == uuid)) {
			remainingSyns = append(remainingSyns, uuid)
			continue // still referenced
		}

		logger.V(1).Info("resource cache purged", "synthesisUUID", uuid)
		delete(c.syntheses, uuid)
	}

	c.synUUIDs[compNSN] = remainingSyns
}

type sliceIndex struct {
	Index     int
	SliceName string
}

type synthesis struct {
	byRef              map[Ref]*Resource
	byIndex            map[sliceIndex]*Resource
	byGroupKind        map[schema.GroupKind][]*Resource
	byDefinedGroupKind map[schema.GroupKind]*Resource
	readinessCursor    int
}
