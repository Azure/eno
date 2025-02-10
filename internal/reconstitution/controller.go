package reconstitution

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
)

// controller reconstitutes individual resources from resource slices.
// Similar to an informer but with extra logic to handle expanding the slice resources.
type controller struct {
	cache           *resource.Cache
	client          client.Client
	nonCachedReader client.Reader
}

func newController(mgr ctrl.Manager, cache *resource.Cache) (*controller, error) {
	r := &controller{
		cache:           cache,
		client:          mgr.GetClient(),
		nonCachedReader: mgr.GetAPIReader(),
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
		r.cache.Purge(ctx, req.NamespacedName, nil)
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)
	ctx = logr.NewContext(ctx, logger)

	// TODO: Requeue after filling
	err = r.populateCache(ctx, comp, comp.Status.PreviousSynthesis)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing previous state: %w", err)
	}

	err = r.populateCache(ctx, comp, comp.Status.CurrentSynthesis)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("processing current state: %w", err)
	}

	r.cache.Purge(ctx, req.NamespacedName, comp)
	return ctrl.Result{}, nil
}

func (r *controller) populateCache(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) error {
	logger := logr.FromContextOrDiscard(ctx)

	if synthesis == nil || synthesis.Synthesized == nil {
		// synthesis is still in progress
		return nil
	}

	if synthesis.UUID == "" {
		logger.V(1).Info("refusing to fill cache because synthesis doesn't have a UUID")
		return nil
	}

	slices := make([]apiv1.ResourceSlice, len(synthesis.ResourceSlices))
	for i, ref := range synthesis.ResourceSlices {
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.nonCachedReader.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return fmt.Errorf("unable to get resource slice: %w", err)
		}
		slices[i] = slice
	}

	logger = logger.WithValues("synthesisCompositionGeneration", synthesis.ObservedCompositionGeneration)
	ctx = logr.NewContext(ctx, logger)
	if r.cache.Visit(ctx, comp, synthesis.UUID, slices) {
		return nil
	}

	for i, ref := range synthesis.ResourceSlices {
		// We use a special non-caching client here because the manifest is removed
		// from resource slices cached in the informer to save memory.
		slice := apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := r.nonCachedReader.Get(ctx, client.ObjectKeyFromObject(&slice), &slice)
		if err != nil {
			return fmt.Errorf("unable to get resource slice: %w", err)
		}
		slices[i] = slice
	}

	r.cache.Fill(ctx, comp, synthesis.UUID, slices)
	return nil
}
