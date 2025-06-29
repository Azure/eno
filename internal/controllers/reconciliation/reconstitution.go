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

// reconstitutionSource implements Eno's concept of "reconstitution": taking partitioned sets of resources
// that were generated during synthesis and handling them as individual controller work items for reconciliation.
//
// It's implemented as an untracked controller that runs as a Source of the reconciliation controller.
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

		// This controller's queue uses composition name/namespace as its key
		skipNameValidation := true
		c, err := controller.NewTypedUnmanaged[reconcile.Request]("reconstitutionController", controller.TypedOptions[reconcile.Request]{
			LogConstructor:     manager.NewTypedLogConstructor[*reconcile.Request](mgr, "reconstitutionController"),
			SkipNameValidation: &skipNameValidation, // Allow duplicate names since we create many dynamic controllers
			Reconciler:         r,
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
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := r.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		r.cache.Purge(ctx, req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}

	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesizerName", comp.Spec.Synthesizer.Name)
	ctx = logr.NewContext(ctx, logger)

	// The reconciliation controller assumes that the previous synthesis will be loaded first
	filled, err := r.populateCache(ctx, comp, comp.Status.PreviousSynthesis)
	if err != nil {
		logger.Error(err, "failed to process previous state")
		return ctrl.Result{}, err
	}
	if filled {
		return ctrl.Result{Requeue: true}, nil
	}

	filled, err = r.populateCache(ctx, comp, comp.Status.CurrentSynthesis)
	if err != nil {
		logger.Error(err, "failed to process current state")
		return ctrl.Result{}, err
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

	// The informers cache an abbreviated representation of the resource slices to save memory
	// We can use them for status but not for spec
	slices := make([]apiv1.ResourceSlice, len(synthesis.ResourceSlices))
	for i, ref := range synthesis.ResourceSlices {
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.client.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return false, client.IgnoreNotFound(fmt.Errorf("unable to get resource slice (cached): %w", err))
		}
		slices[i] = slice
	}

	compNSN := types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}
	if r.cache.Visit(ctx, comp, synthesis.UUID, slices) {
		return false, nil
	}

	// Get the full resource slices to populate the cache
	// But don't use the status since it might be ahead of the informer
	for i, ref := range synthesis.ResourceSlices {
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.nonCachedReader.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return false, client.IgnoreNotFound(fmt.Errorf("unable to get resource slice (no cache): %w", err))
		}
		slices[i] = slice
	}

	r.cache.Fill(ctx, compNSN, synthesis.UUID, slices)
	return true, nil
}
