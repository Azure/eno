package resource

import (
	"context"
	"math"
	"sync"
	"sync/atomic"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/readiness"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
)

// TODO: Type safety for no status to Fill? Remember to requeue after filling!

// TODO: The executor needs to set CRD readiness groups

// TODO: Remove requeue from reconciliation controller

type Request struct {
	Resource    Ref
	Composition types.NamespacedName
}

type Cache struct {
	renv  *readiness.Env
	queue workqueue.TypedRateLimitingInterface[*Request]

	mut       sync.Mutex
	syntheses map[string]*Synthesis
	synUUIDs  map[types.NamespacedName][]string // by composition
}

func NewCache(renv *readiness.Env, queue workqueue.TypedRateLimitingInterface[*Request]) *Cache {
	return &Cache{
		renv:      renv,
		queue:     queue,
		syntheses: map[string]*Synthesis{},
		synUUIDs:  map[types.NamespacedName][]string{},
	}
}

func (c *Cache) Get(uuid string) (*Synthesis, bool) {
	c.mut.Lock()
	defer c.mut.Unlock()
	syn, ok := c.syntheses[uuid]
	return syn, ok
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
	var minReadyGrp int = math.MaxInt       // the lowest known readiness group (not considering status)
	var maxReadyReadyGrp int = -math.MaxInt // the highest readiness group that is currently ready
	for _, slice := range items {
		for i, state := range slice.Status.Resources {
			res, ok := snap.byIndex[sliceIndex{Index: i, SliceName: slice.Name}]
			if !ok {
				continue
			}
			if res.ReadinessGroup < minReadyGrp {
				minReadyGrp = res.ReadinessGroup
			}
			if (res.lastState == nil || res.lastState.Ready == nil) && res.ReadinessGroup > maxReadyReadyGrp {
				maxReadyReadyGrp = res.ReadinessGroup
			}
			if res.VisitState(&state) {
				c.queue.Add(&Request{Resource: res.Ref, Composition: compNSN})
			}
		}
	}

	// Find the new readiness cursor
	cursor := max(minReadyGrp, maxReadyReadyGrp)
	oldCursor := int(snap.readinessCursor.Swap(int64(cursor)))
	if cursor > oldCursor {
		logger.V(1).Info("readiness cursor advanced", "synthesisUUID", synUUID, "oldCursor", oldCursor, "newCursor", cursor)
	} else {
		return true
	}

	// Enqueue any readiness groups that are now visible/ready
	for _, slice := range items {
		for i, state := range slice.Status.Resources {
			res, ok := snap.byIndex[sliceIndex{Index: i, SliceName: slice.Name}]
			if !ok {
				continue
			}
			if !state.Reconciled && res.ReadinessGroup <= cursor && res.ReadinessGroup > oldCursor {
				c.queue.Add(&Request{Resource: res.Ref, Composition: compNSN})
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
	requests := []*Request{}
	snap := &Synthesis{
		byRef:   map[Ref]*Resource{},
		byIndex: map[sliceIndex]*Resource{},
	}
	snap.readinessCursor.Store(-math.MaxInt)
	for _, slice := range items {
		slice := slice
		for i := range slice.Spec.Resources {
			res, err := NewResource(ctx, c.renv, &slice, i)
			if err != nil {
				logger.Error(err, "failed to create resource", "resourceSliceName", slice.Name, "resourceIndex", i)
				return
			}

			snap.byRef[res.Ref] = res
			snap.byIndex[sliceIndex{Index: i, SliceName: slice.Name}] = res
			requests = append(requests, &Request{Resource: res.Ref, Composition: compNSN})
		}
	}

	c.mut.Lock()
	c.syntheses[synUUID] = snap
	c.synUUIDs[compNSN] = append(c.synUUIDs[compNSN], synUUID)
	c.mut.Unlock()
	logger.V(1).Info("resource cache filled", "synthesisUUID", synUUID)

	for _, req := range requests {
		c.queue.Add(req)
	}
}

// Purge removes all cache state for the composition referenced by compNSN.
// If comp is not nil, syntheses still referenced by the composition will be preserved.
func (c *Cache) Purge(ctx context.Context, compNSN types.NamespacedName, comp *apiv1.Composition) {
	logger := logr.FromContextOrDiscard(ctx)
	c.mut.Lock()
	defer c.mut.Unlock()

	remainingSyns := []string{}
	for _, uuid := range c.synUUIDs[compNSN] {
		// Don't touch any syntheses still referenced by the composition
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

type Synthesis struct {
	byRef           map[Ref]*Resource
	byIndex         map[sliceIndex]*Resource
	readinessCursor atomic.Int64 // the max readiness group that is currently ready
}

// TODO: Think about concurrency around gets and status changes

func (s *Synthesis) Get(ref *Ref) (*Resource, bool) {
	res, ok := s.byRef[*ref]
	return res, ok
}

func (s *Synthesis) GetByIndex(sliceName string, idx int) (*Resource, bool) {
	res, ok := s.byIndex[sliceIndex{Index: idx, SliceName: sliceName}]
	return res, ok
}

func (s *Synthesis) ReadinessGroupIsReady(grp int) bool {
	return int(s.readinessCursor.Load()) >= grp
}
