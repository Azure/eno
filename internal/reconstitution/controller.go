package reconstitution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

// TODO: Resolve secret references

var ErrNotFound = errors.New("resource not found")

type Reconciler interface {
	Reconcile(ctx context.Context, req *GeneratedResourceMeta) (ctrl.Result, error)
}

type Client interface {
	Get(ctx context.Context, gen int64, req *GeneratedResourceMeta) (*GeneratedResource, error)
	UpdateStatus(context.Context, *GeneratedResourceMeta, *GeneratedResourceStatus) error
}

// New creates a new Manager, which is responsible for "reconstituting" generated resources
// i.e. allowing controllers to treat them as individual resources instead of their storage representation (GeneratedResourceSlice).
func New(mgr ctrl.Manager) (*Manager, error) {
	m := &Manager{
		Manager: mgr,
		recon: &reconstituter{
			Client:                       mgr.GetClient(),
			resources:                    make(map[resourceKey]*GeneratedResource),
			attemptsByGeneration:         make(map[types.NamespacedName][]int64),
			resourcesByGenerationAttempt: map[generationKey][]resourceKey{},
		},
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.Generation{}).
		Owns(&apiv1.GeneratedResourceSlice{}).
		Build(m.recon)
	if err != nil {
		return nil, err
	}

	return nil, nil
}

type Manager struct {
	ctrl.Manager
	recon *reconstituter
}

func (m *Manager) GetClient() Client { return m.recon }

func (m *Manager) Add(rec Reconciler) error {
	rateLimiter := workqueue.DefaultControllerRateLimiter()
	queue := workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{
		Name: "generatedResourceReconciler",
	})
	qp := &queueProcessor{
		Client:  m.Manager.GetClient(),
		Queue:   queue,
		Recon:   m.recon,
		Handler: rec,
	}
	m.recon.Queues = append(m.recon.Queues, qp.Queue)
	return m.Manager.Add(qp)
}

type queueProcessor struct {
	Client  client.Client
	Queue   workqueue.RateLimitingInterface
	Recon   *reconstituter
	Handler Reconciler
}

func (q *queueProcessor) Start(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		q.Queue.ShutDown()
	}()
	for q.processQueueItem(ctx) {
	}
	return nil
}

func (q *queueProcessor) processQueueItem(ctx context.Context) bool {
	item, shutdown := q.Queue.Get()
	if shutdown {
		return false
	}
	defer q.Queue.Done(item)

	req := item.(*GeneratedResourceMeta)
	result, err := q.Handler.Reconcile(ctx, req)
	if err != nil {
		q.Queue.AddRateLimited(item)
		return true
	}
	if result.RequeueAfter != 0 {
		q.Queue.Forget(item)
		q.Queue.AddAfter(item, result.RequeueAfter)
		return true
	}

	q.Queue.Forget(item)
	return true
}

type reconstituter struct {
	Client client.Client
	Queues []workqueue.Interface

	mut                          sync.Mutex
	resources                    map[resourceKey]*GeneratedResource
	attemptsByGeneration         map[types.NamespacedName][]int64
	resourcesByGenerationAttempt map[generationKey][]resourceKey
}

func (r *reconstituter) Get(ctx context.Context, gen int64, req *GeneratedResourceMeta) (*GeneratedResource, error) {
	r.mut.Lock()
	defer r.mut.Unlock()

	// res, ok := r.byReq[*req]
	// if !ok {
	// 	return nil, ErrNotFound
	// }
	// return res, nil
	return nil, nil
}

func (r *reconstituter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	gen := &apiv1.Generation{}
	err := r.Client.Get(ctx, req.NamespacedName, gen)
	if k8serrors.IsNotFound(err) {
		r.purgeDanglingResources(req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	err = r.populateCache(ctx, gen, gen.Status.PreviousState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}

	err = r.populateCache(ctx, gen, gen.Status.CurrentState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}

	r.purgeDanglingResources(req.NamespacedName, gen)
	return ctrl.Result{}, nil
}

func (r *reconstituter) populateCache(ctx context.Context, gen *apiv1.Generation, attempt *apiv1.GenerationAttempt) error {
	if attempt == nil {
		return nil
	}

	key := generationKey{Namespace: gen.Namespace, Name: gen.Name, Generation: attempt.ObservedGeneration}

	r.mut.Lock()
	_, exists := r.resourcesByGenerationAttempt[key]
	r.mut.Unlock()

	if exists {
		return nil // already cached
	}

	slices := &apiv1.GeneratedResourceSliceList{}
	err := r.Client.List(ctx, slices, client.MatchingFieldsSelector{
		// TODO: Index to match only this generation
	})
	if err != nil {
		return fmt.Errorf("listing resource slices: %w", err)
	}
	if int64(len(slices.Items)) != attempt.ResourceSliceCount {
		return nil // wait for the cache to be fully populated
	}

	// Build our internal representation of each resource
	resources := map[resourceKey]*GeneratedResource{}
	for _, slice := range slices.Items {

		// NOTE: In the future we can build a DAG here to find edges between dependant resources

		for _, resource := range slice.Spec.Resources {
			resource := resource
			gr, err := r.buildGeneratedResource(ctx, &resource)
			if err != nil {
				continue // skip invalid resources
			}
			key := newResourceKey(slice.Spec.GenerationGeneration, gr)
			resources[key] = gr
		}
	}

	r.mut.Lock()
	defer r.mut.Unlock()

	// Store items and notify listeners
	_, exists = r.resourcesByGenerationAttempt[key]
	if exists {
		return nil // extreme edge case - only possible if concurrency is somehow > 1
	}

	nsn := types.NamespacedName{Namespace: gen.Namespace, Name: gen.Name}
	r.attemptsByGeneration[nsn] = append(r.attemptsByGeneration[nsn], attempt.ObservedGeneration)

	keys := []resourceKey{}
	for key, gr := range resources {
		keys = append(keys, key)
		r.resources[key] = gr
		r.enqueue(gr.Meta)
	}
	r.resourcesByGenerationAttempt[key] = keys

	return nil
}

func (r *reconstituter) buildGeneratedResource(ctx context.Context, resource *apiv1.GeneratedResourceSpec) (*GeneratedResource, error) {
	parsed := &unstructured.Unstructured{}
	err := parsed.UnmarshalJSON([]byte(resource.Manifest))
	if err != nil {
		return nil, fmt.Errorf("invalid json: %w", err)
	}

	gr := &GeneratedResource{
		Meta: &GeneratedResourceMeta{
			Namespace: parsed.GetNamespace(),
			Name:      parsed.GetName(),
			Kind:      parsed.GetKind(),
		},
		Spec: &GeneratedResourceSpec{
			Manifest: resource.Manifest,
			Object:   parsed,
		},
		Status: &GeneratedResourceStatus{
			// TODO: ?
		},
	}
	if resource.ReconcileInterval != nil {
		gr.Spec.ReconcileInterval = resource.ReconcileInterval.Duration
	}
	if gr.Meta.Name == "" || gr.Meta.Kind == "" {
		return nil, fmt.Errorf("missing name or kind")
	}
	return gr, nil
}

func (r *reconstituter) enqueue(meta *GeneratedResourceMeta) {
	for _, q := range r.Queues {
		q.Add(meta)
	}
}

func (r *reconstituter) purgeDanglingResources(nsn types.NamespacedName, gen *apiv1.Generation) {
	r.mut.Lock()
	defer r.mut.Unlock()

	attemptGens := r.attemptsByGeneration[nsn]
	newGens := []int64{}
	for _, attemptGen := range attemptGens {
		if gen != nil && ((gen.Status.CurrentState != nil && gen.Status.CurrentState.ObservedGeneration == attemptGen) || (gen.Status.PreviousState != nil && gen.Status.PreviousState.ObservedGeneration == attemptGen)) {
			newGens = append(newGens, attemptGen)
			continue // still referenced by the Generation
		}

		genKey := generationKey{
			Namespace:  nsn.Namespace,
			Name:       nsn.Name,
			Generation: attemptGen,
		}

		resources := r.resourcesByGenerationAttempt[genKey]
		for _, key := range resources {
			delete(r.resources, key)
		}

		delete(r.resourcesByGenerationAttempt, genKey)
	}
	if len(attemptGens) == 0 {
		delete(r.attemptsByGeneration, nsn)
	} else {
		r.attemptsByGeneration[nsn] = attemptGens
	}
}

func (r *reconstituter) UpdateStatus(context.Context, *GeneratedResourceMeta, *GeneratedResourceStatus) error {
	return nil // TODO: Use work queue for batching? Re-enqueue in main queue on failure/conflict to retry, add slice resource version private to req
	// TODO: Weird edge case: we need to keep track of pending writes to honor the resource version cache
}

type resourceKey struct {
	Namespace, Name, Kind string
	GenerationGeneration  int64 // metadata.generation of the parent Generation resource
}

func newResourceKey(gen int64, gr *GeneratedResource) resourceKey {
	return resourceKey{
		Namespace:            gr.Meta.Namespace,
		Name:                 gr.Meta.Name,
		Kind:                 gr.Meta.Kind,
		GenerationGeneration: gen,
	}
}

type generationKey struct {
	Namespace, Name string
	Generation      int64
}
