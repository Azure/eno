package reconstitution

import (
	"context"
	"fmt"
	"strconv"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
)

type reconstituter struct {
	*cache
	Client client.Client
	Queues []workqueue.Interface
	Logger logr.Logger
}

func (r *reconstituter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Logger.V(1).WithValues("composition", req).Info("caching composition")

	comp := &apiv1.Composition{}
	err := r.Client.Get(ctx, req.NamespacedName, comp)
	if k8serrors.IsNotFound(err) {
		r.cache.Purge(ctx, req.NamespacedName, nil)
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

	r.cache.Purge(ctx, req.NamespacedName, comp)
	return ctrl.Result{}, nil
}

func (r *reconstituter) populateCache(ctx context.Context, comp *apiv1.Composition, synthesis *apiv1.Synthesis) error {
	logger := logr.FromContextOrDiscard(ctx)

	if synthesis == nil {
		return nil
	}

	logger = logger.WithValues("synthesisGen", synthesis.ObservedGeneration)
	ctx = logr.NewContext(ctx, logger)
	if r.cache.Exists(comp, synthesis) {
		logger.V(1).Info("this synthesis has already been cached")
		return nil
	}

	slices := &apiv1.ResourceSliceList{}
	err := r.Client.List(ctx, slices, client.MatchingFields{
		"spec.compositionGeneration": strconv.FormatInt(synthesis.ObservedGeneration, 10),
		// TODO: Need to merge these selectors
		// "metadata.ownerReferences.name": comp.Name,
	})
	if err != nil {
		return fmt.Errorf("listing resource slices: %w", err)
	}

	logger.V(1).Info(fmt.Sprintf("found %d slices", len(slices.Items)))
	if int64(len(slices.Items)) != synthesis.ResourceSliceCount {
		logger.V(1).Info("stale informer - waiting for sync")
		return nil
	}

	reqs, err := r.cache.Fill(ctx, comp, synthesis, slices.Items)
	if err != nil {
		return err
	}
	for _, req := range reqs {
		for _, queue := range r.Queues {
			queue.Add(req)
		}
	}

	return nil
}
