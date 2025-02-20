package reconciliation

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type reconstitutionSource struct {
	client          client.Client
	nonCachedReader client.Reader
	cache           *resource.Cache
}

func newReconstitutionSource(mgr ctrl.Manager) (source.TypedSource[resource.Request], *resource.Cache, error) {
	var cache resource.Cache
	return source.TypedFunc[resource.Request](func(ctx context.Context, queue workqueue.TypedRateLimitingInterface[resource.Request]) error {
		cache.SetQueue(queue)

		r := &reconstitutionSource{
			client:          mgr.GetClient(),
			nonCachedReader: mgr.GetAPIReader(),
			cache:           &cache,
		}

		c, err := controller.NewTypedUnmanaged[reconcile.Request]("reconstitutionController", mgr, controller.TypedOptions[reconcile.Request]{
			LogConstructor: manager.NewTypedLogConstructor[*reconcile.Request](mgr, "reconstitutionController"),
			Reconciler:     r,
		})
		if err != nil {
			return err
		}

		err = c.Watch(source.TypedKind(mgr.GetCache(), &apiv1.Composition{}, &handler.TypedEnqueueRequestForObject[*apiv1.Composition]{}))
		if err != nil {
			return err
		}
		err = c.Watch(source.TypedKind(mgr.GetCache(), &apiv1.ResourceSlice{}, handler.TypedEnqueueRequestForOwner[*apiv1.ResourceSlice](mgr.GetScheme(), mgr.GetRESTMapper(), &apiv1.Composition{})))
		if err != nil {
			return err
		}

		go func() {
			err := c.Start(ctx)
			if err != nil {
				panic(fmt.Sprintf("error while starting reconstitution source controller: %s", err))
			}
		}()

		return nil
	}), &cache, nil
}

func (r *reconstitutionSource) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := r.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		r.cache.Purge(ctx, req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace)
	ctx = logr.NewContext(ctx, logger)

	filled, err := r.populateCache(ctx, comp, comp.Status.PreviousSynthesis)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}
	if filled {
		return ctrl.Result{Requeue: true}, nil
	}

	filled, err = r.populateCache(ctx, comp, comp.Status.CurrentSynthesis)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}
	if filled {
		return ctrl.Result{Requeue: true}, nil
	}

	r.cache.Purge(ctx, req.NamespacedName, comp)
	return ctrl.Result{}, nil
}

func (r *reconstitutionSource) populateCache(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) (bool, error) {
	if synthesis == nil || synthesis.Synthesized == nil {
		// synthesis is still in progress
		return false, nil
	}

	slices := make([]apiv1.ResourceSlice, len(synthesis.ResourceSlices))
	for i, ref := range synthesis.ResourceSlices {
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.client.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return false, fmt.Errorf("unable to get resource slice: %w", err)
		}
		slices[i] = slice
	}

	compNSN := types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}
	if r.cache.Visit(ctx, comp, synthesis.UUID, slices) {
		// TODO: Requeue?
		return false, nil
	}

	for i, ref := range synthesis.ResourceSlices {
		// We use a special non-caching client here because the manifest is removed
		// from resource slices cached in the informer to save memory.
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.nonCachedReader.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return false, fmt.Errorf("unable to get resource slice: %w", err)
		}
		slices[i] = slice
	}

	r.cache.Fill(ctx, compNSN, synthesis.UUID, slices)
	return true, nil
}
