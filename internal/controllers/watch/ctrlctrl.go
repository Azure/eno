package watch

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/ratelimiter"

	apiv1 "github.com/Azure/eno/api/v1"
)

// controllerController manages a pool of status controllers for every group/kind ref'd by a synthesizer.
type controllerController struct {
	mgr            ctrl.Manager
	client         client.Client
	refControllers map[apiv1.ResourceRef]*refStatusController
	limiter        ratelimiter.RateLimiter
}

func (c *controllerController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	synths := &apiv1.SynthesizerList{}
	err := c.client.List(ctx, synths)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Start any missing controllers
	synthsByRef := map[apiv1.ResourceRef]struct{}{}
	for _, syn := range synths.Items {
		if syn.DeletionTimestamp != nil {
			continue
		}
		for _, ref := range syn.Spec.Refs {
			ref := ref
			synthsByRef[ref.Resource] = struct{}{}

			current := c.refControllers[ref.Resource]
			if current != nil {
				continue // already running
			}

			rc, err := newRefStatusController(ctx, c, &ref.Resource)
			if err != nil {
				return ctrl.Result{}, err
			}
			c.refControllers[ref.Resource] = rc
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// Stop controllers that are no longer needed
	for ref, rc := range c.refControllers {
		if _, ok := synthsByRef[ref]; ok {
			continue
		}

		rc.Stop(ctx)
		delete(c.refControllers, ref)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}
