package reconstitution

import (
	"context"
	"fmt"
	"sync/atomic"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

const indexName = ".metadata.owner"

// reconstituter reconstitutes individual resources out of resource slices.
// Similar to an informer but with extra logic to handle expanding the slice resources.
type reconstituter struct {
	*cache  // embedded because caching is logically part of the reconstituter's functionality
	client  client.Client
	queues  []workqueue.Interface
	logger  logr.Logger
	started atomic.Bool
}

func newReconstituter(mgr ctrl.Manager) (*reconstituter, error) {
	// Index resource slices by the specific synthesis they originate from
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, indexName, func(o client.Object) []string {
		slice := o.(*apiv1.ResourceSlice)
		owner := metav1.GetControllerOf(slice)
		if owner == nil || owner.Kind != "Composition" {
			return nil
		}
		// keys will not collide because k8s doesn't allow slashes in names
		return []string{fmt.Sprintf("%s/%d", owner.Name, slice.Spec.CompositionGeneration)}
	})
	if err != nil {
		return nil, err
	}

	r := &reconstituter{
		cache:  newCache(mgr.GetClient()),
		client: mgr.GetClient(),
		logger: mgr.GetLogger(),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		Build(r)
	return r, err
}

func (r *reconstituter) AddQueue(queue workqueue.Interface) {
	if r.started.Load() {
		panic("AddQueue must be called before any resources are reconciled")
	}
	r.queues = append(r.queues, queue)
}

func (r *reconstituter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.started.Store(true)
	r.logger.V(1).WithValues("composition", req).Info("caching composition")

	comp := &apiv1.Composition{}
	err := r.client.Get(ctx, req.NamespacedName, comp)
	if k8serrors.IsNotFound(err) {
		r.cache.Purge(ctx, req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	// We populate the cache with both the previous and current syntheses
	err = r.populateCache(ctx, comp, comp.Status.PreviousState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}
	err = r.populateCache(ctx, comp, comp.Status.CurrentState)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}

	r.cache.Purge(ctx, req.NamespacedName, comp)
	return ctrl.Result{}, nil
}

func (r *reconstituter) populateCache(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) error {
	logger := logr.FromContextOrDiscard(ctx)

	if synthesis == nil {
		return nil
	}
	compNSN := types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}

	logger = logger.WithValues("synthesisGen", synthesis.ObservedGeneration)
	ctx = logr.NewContext(ctx, logger)
	if r.cache.HasSynthesis(ctx, compNSN, synthesis) {
		logger.V(1).Info("this synthesis has already been cached")
		return nil
	}

	slices := &apiv1.ResourceSliceList{}
	err := r.client.List(ctx, slices, client.InNamespace(comp.Namespace), client.MatchingFields{
		indexName: fmt.Sprintf("%s/%d", comp.Name, synthesis.ObservedGeneration),
	})
	if err != nil {
		return fmt.Errorf("listing resource slices: %w", err)
	}

	logger.V(1).Info(fmt.Sprintf("found %d slices for this synthesis", len(slices.Items)))
	if int64(len(slices.Items)) != synthesis.ResourceSliceCount {
		logger.V(1).Info("stale informer - waiting for sync")
		return nil
	}

	reqs, err := r.cache.Fill(ctx, compNSN, synthesis, slices.Items)
	if err != nil {
		return err
	}
	for _, req := range reqs {
		for _, queue := range r.queues {
			queue.Add(req)
		}
	}

	return nil
}
