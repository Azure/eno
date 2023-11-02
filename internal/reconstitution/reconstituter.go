package reconstitution

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

type reconstituter struct {
	Client client.Client
	Queues []workqueue.Interface

	mut                    sync.Mutex
	resources              map[resourceKey]*Resource
	synthesesByComposition map[types.NamespacedName][]int64
	resourcesBySynthesis   map[synthesisKey][]resourceKey
}

func (r *reconstituter) Get(ctx context.Context, gen int64, meta *ResourceMeta) (*Resource, error) {
	r.mut.Lock()
	defer r.mut.Unlock()

	res, ok := r.resources[resourceKey{
		ResourceMeta:          *meta,
		CompositionGeneration: gen,
	}]
	if !ok {
		return nil, ErrNotFound
	}
	return res, nil
}

func (r *reconstituter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := r.Client.Get(ctx, req.NamespacedName, comp)
	if k8serrors.IsNotFound(err) {
		r.purgeDanglingResources(req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	err = r.populateCache(ctx, comp, comp.Status.PreviousState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}

	err = r.populateCache(ctx, comp, comp.Status.CurrentState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}

	r.purgeDanglingResources(req.NamespacedName, comp)
	return ctrl.Result{}, nil
}

func (r *reconstituter) populateCache(ctx context.Context, comp *apiv1.Composition, attempt *apiv1.Synthesis) error {
	if attempt == nil {
		return nil
	}

	key := synthesisKey{Namespace: comp.Namespace, Name: comp.Name, CompositionGeneration: attempt.ObservedGeneration}

	r.mut.Lock()
	_, exists := r.resourcesBySynthesis[key]
	r.mut.Unlock()

	if exists {
		return nil // already cached
	}

	slices := &apiv1.ResourceSliceList{}
	err := r.Client.List(ctx, slices, client.MatchingFields{
		"spec.compositionGeneration":    strconv.FormatInt(attempt.ObservedGeneration, 10),
		"metadata.ownerReferences.name": comp.Name,
	})
	if err != nil {
		return fmt.Errorf("listing resource slices: %w", err)
	}
	if int64(len(slices.Items)) != attempt.ResourceSliceCount {
		return nil // wait for the cache to be fully populated
	}

	// Build our internal representation of each resource
	resources := map[resourceKey]*Resource{}
	for _, slice := range slices.Items {

		// NOTE: In the future we can build a DAG here to find edges between dependant resources

		for _, resource := range slice.Spec.Resources {
			resource := resource
			gr, err := r.buildResource(ctx, &resource)
			if err != nil {
				continue // skip invalid resources
			}
			key := resourceKey{
				ResourceMeta:          *gr.Meta,
				CompositionGeneration: slice.Spec.CompositionGeneration,
			}
			resources[key] = gr
		}
	}

	r.mut.Lock()
	defer r.mut.Unlock()

	// Store items and notify listeners
	_, exists = r.resourcesBySynthesis[key]
	if exists {
		return nil // extreme edge case - only possible if concurrency is somehow > 1
	}

	nsn := types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}
	r.synthesesByComposition[nsn] = append(r.synthesesByComposition[nsn], attempt.ObservedGeneration)

	keys := []resourceKey{}
	for rk, gr := range resources {
		keys = append(keys, rk)
		r.resources[rk] = gr
		r.enqueue(&key, gr)
	}
	r.resourcesBySynthesis[key] = keys

	return nil
}

func (r *reconstituter) buildResource(ctx context.Context, resource *apiv1.ResourceSpec) (*Resource, error) {
	parsed := &unstructured.Unstructured{}
	err := parsed.UnmarshalJSON([]byte(resource.Manifest))
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	// TODO: Fetch the manifests of sensitive resources (e.g. secrets) from secrets referenced by the resource

	gr := &Resource{
		Meta: &ResourceMeta{
			Namespace: parsed.GetNamespace(),
			Name:      parsed.GetName(),
			Kind:      parsed.GetKind(),
		},
		Manifest: resource.Manifest,
		Object:   parsed,
	}
	if resource.ReconcileInterval != nil {
		gr.ReconcileInterval = resource.ReconcileInterval.Duration
	}
	if gr.Meta.Name == "" || gr.Meta.Kind == "" {
		return nil, fmt.Errorf("missing name or kind")
	}
	return gr, nil
}

func (r *reconstituter) enqueue(key *synthesisKey, gr *Resource) {
	for _, q := range r.Queues {
		q.Add(&Request{
			ResourceMeta: *gr.Meta,
			Composition: types.NamespacedName{
				Namespace: key.Namespace,
				Name:      key.Name,
			},
		})
	}
}

func (r *reconstituter) purgeDanglingResources(nsn types.NamespacedName, comp *apiv1.Composition) {
	r.mut.Lock()
	defer r.mut.Unlock()

	synGens := r.synthesesByComposition[nsn]
	newGens := []int64{}
	for _, gen := range synGens {
		if comp != nil && ((comp.Status.CurrentState != nil && comp.Status.CurrentState.ObservedGeneration == gen) || (comp.Status.PreviousState != nil && comp.Status.PreviousState.ObservedGeneration == gen)) {
			newGens = append(newGens, gen)
			continue // still referenced by the Generation
		}

		synKey := synthesisKey{
			Namespace:             nsn.Namespace,
			Name:                  nsn.Name,
			CompositionGeneration: gen,
		}

		resources := r.resourcesBySynthesis[synKey]
		for _, key := range resources {
			delete(r.resources, key)
		}

		delete(r.resourcesBySynthesis, synKey)
	}
	if len(synGens) == 0 {
		delete(r.synthesesByComposition, nsn)
	} else {
		r.synthesesByComposition[nsn] = synGens
	}
}
