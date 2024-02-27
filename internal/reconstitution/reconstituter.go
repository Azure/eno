package reconstitution

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// reconstituter reconstitutes individual resources from resource slices.
// Similar to an informer but with extra logic to handle expanding the slice resources.
type reconstituter struct {
	*cache  // embedded because caching is logically part of the reconstituter's functionality
	client  client.Client
	queues  []workqueue.Interface
	started atomic.Bool
}

func newReconstituter(mgr ctrl.Manager) (*reconstituter, error) {
	r := &reconstituter{
		cache:  newCache(mgr.GetClient()),
		client: mgr.GetClient(),
	}

	return r, ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "reconstituter")).
		Complete(r)
}

func (r *reconstituter) AddQueue(queue workqueue.Interface) {
	if r.started.Load() {
		panic("AddQueue must be called before any resources are reconciled")
	}
	r.queues = append(r.queues, queue)
}

func (r *reconstituter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.started.Store(true)

	comp := &apiv1.Composition{}
	err := r.client.Get(ctx, req.NamespacedName, comp)
	if k8serrors.IsNotFound(err) {
		r.cache.Purge(ctx, req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)
	ctx = logr.NewContext(ctx, logger)

	// We populate the cache with both the previous and current syntheses
	prevReqs, err := r.populateCache(ctx, comp, comp.Status.PreviousState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}
	currentReqs, err := r.populateCache(ctx, comp, comp.Status.CurrentState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}
	for _, req := range append(prevReqs, currentReqs...) {
		for _, queue := range r.queues {
			queue.Add(req)
		}
	}
	r.cache.Purge(ctx, req.NamespacedName, comp)

	if len(currentReqs)+len(prevReqs) > 0 {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *reconstituter) populateCache(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) ([]*Request, error) {
	logger := logr.FromContextOrDiscard(ctx)

	if synthesis == nil || !synthesis.Synthesized {
		// synthesis is still in progress
		return nil, nil
	}

	logger = logger.WithValues("synthesisCompositionGeneration", synthesis.ObservedCompositionGeneration)
	ctx = logr.NewContext(ctx, logger)
	if r.cache.HasSynthesis(ctx, comp, synthesis) {
		return nil, nil
	}

	slices := make([]apiv1.ResourceSlice, len(synthesis.ResourceSlices))
	for i, ref := range synthesis.ResourceSlices {
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.client.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return nil, fmt.Errorf("unable to get resource slice: %w", err)
		}
		slices[i] = slice
	}

	return r.cache.Fill(ctx, comp, synthesis, slices)
}
