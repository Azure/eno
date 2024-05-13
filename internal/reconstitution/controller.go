package reconstitution

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

// controller reconstitutes individual resources from resource slices.
// Similar to an informer but with extra logic to handle expanding the slice resources.
type controller struct {
	*Cache          // embedded because caching is logically part of the reconstituter's functionality
	client          client.Client
	nonCachedReader client.Reader
	queue           workqueue.RateLimitingInterface
}

func newController(mgr ctrl.Manager, cache *Cache) (*controller, error) {
	r := &controller{
		Cache:           cache,
		client:          mgr.GetClient(),
		nonCachedReader: mgr.GetAPIReader(),
	}
	rateLimiter := workqueue.DefaultItemBasedRateLimiter()
	r.queue = workqueue.NewRateLimitingQueueWithConfig(rateLimiter, workqueue.RateLimitingQueueConfig{
		Name: "reconciliationController",
	})

	err := ctrl.NewControllerManagedBy(mgr).
		Named("readinessTransitionResponder").
		For(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "readinessTransitionResponder")).
		Complete(reconcile.Func(r.HandleReadinessTransition))
	if err != nil {
		return nil, err
	}

	return r, ctrl.NewControllerManagedBy(mgr).
		Named("reconstituter").
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "reconstituter")).
		Complete(r)
}

func (r *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := r.client.Get(ctx, req.NamespacedName, comp)
	if k8serrors.IsNotFound(err) {
		r.Cache.purge(req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)
	ctx = logr.NewContext(ctx, logger)

	// We populate the cache with both the previous and current syntheses
	prevReqs, err := r.populateCache(ctx, comp, comp.Status.PreviousSynthesis)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}
	currentReqs, err := r.populateCache(ctx, comp, comp.Status.CurrentSynthesis)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}
	for _, req := range append(prevReqs, currentReqs...) {
		r.queue.Add(*req)
	}
	r.Cache.purge(req.NamespacedName, comp)

	if len(currentReqs)+len(prevReqs) > 0 {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

func (r *controller) populateCache(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) ([]*Request, error) {
	logger := logr.FromContextOrDiscard(ctx)

	if synthesis == nil || synthesis.Synthesized == nil {
		// synthesis is still in progress
		return nil, nil
	}

	logger = logger.WithValues("synthesisCompositionGeneration", synthesis.ObservedCompositionGeneration)
	ctx = logr.NewContext(ctx, logger)
	if r.Cache.hasSynthesis(comp, synthesis) {
		return nil, nil
	}

	slices := make([]apiv1.ResourceSlice, len(synthesis.ResourceSlices))
	for i, ref := range synthesis.ResourceSlices {
		// We use a special non-caching client here because the manifest is removed
		// from resource slices cached in the informer to save memory.
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.nonCachedReader.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return nil, client.IgnoreNotFound(fmt.Errorf("unable to get resource slice: %w", err))
		}
		slices[i] = slice
	}

	return r.Cache.fill(ctx, comp, synthesis, slices)
}

func (r *controller) HandleReadinessTransition(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	slice := &apiv1.ResourceSlice{}
	err := r.client.Get(ctx, req.NamespacedName, slice)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("unable to get resource slice: %w", err))
	}

	owner := metav1.GetControllerOf(slice)
	if owner == nil {
		return ctrl.Result{}, nil
	}

	for i, res := range slice.Status.Resources {
		if res.Ready == nil {
			continue // only care about resources that have become ready
		}

		res, ok := r.Cache.getByIndex(&sliceIndex{
			Index:     i,
			SliceName: slice.Name,
			Namespace: slice.Namespace,
		})
		if !ok {
			logger.V(1).Info("a dependent resource was not found in cache - this is unexpected")
			return ctrl.Result{}, nil
		}

		synRef := &SynthesisRef{CompositionName: owner.Name, Namespace: req.Namespace, UUID: slice.Spec.SynthesisUUID}
		resources := r.Cache.RangeByReadinessGroup(ctx, synRef, res.ReadinessGroup, RangeAsc)
		if res.DefinedGroupKind != nil {
			resources = append(resources, r.Cache.getByGK(synRef, *res.DefinedGroupKind)...)
		}
		for _, res := range resources {
			// TODO: This can be optimized by skipping the Add call if `res` is already ready
			r.queue.Add(Request{
				Resource:    res.Ref,
				Composition: types.NamespacedName{Namespace: slice.Namespace, Name: owner.Name},
			})
		}
	}

	return ctrl.Result{}, nil
}
