package watch

import (
	"context"
	"math/rand"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type WatchController struct {
	mgr            ctrl.Manager
	client         client.Client
	refControllers map[apiv1.ResourceRef]*KindWatchController
}

func NewController(mgr ctrl.Manager) error {
	err := ctrl.NewControllerManagedBy(mgr).
		Named("watchControllerController").
		Watches(&apiv1.Synthesizer{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "watchController")).
		Complete(&WatchController{
			mgr:            mgr,
			client:         mgr.GetClient(),
			refControllers: map[apiv1.ResourceRef]*KindWatchController{},
		})
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "watchPruningController")).
		Complete(&pruningController{
			client: mgr.GetClient(),
		})
}

func (c *WatchController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	synths := &apiv1.SynthesizerList{}
	err := c.client.List(ctx, synths)
	if err != nil {
		return ctrl.Result{}, err
	}

	// It's important to randomize the order over which we iterate the synths,
	// otherwise one bad resource reference can block the loop
	rand.Shuffle(len(synths.Items), func(i, j int) { synths.Items[i], synths.Items[j] = synths.Items[j], synths.Items[i] })

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

			rc, err := NewKindWatchController(ctx, c, &ref.Resource)
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
