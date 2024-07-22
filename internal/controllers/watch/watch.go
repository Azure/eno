package watch

import (
	"context"
	"fmt"
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

	names := []string{}
	for _, syn := range synths.Items {
		names = append(names, syn.Name)
	}
	c.mgr.GetLogger().Info(fmt.Sprintf("TODO starting to reconcile %+s", names))

	// It's important to randomize the order over which we iterate the synths,
	// otherwise one bad resource reference can block the loop
	rand.Shuffle(len(synths.Items), func(i, j int) { synths.Items[i] = synths.Items[j] })

	// Start any missing controllers
	synthsByRef := map[apiv1.ResourceRef]struct{}{}
	for _, syn := range synths.Items {
		if syn.DeletionTimestamp != nil {
			c.mgr.GetLogger().Info("TODO skipping deleted")
			continue
		}
		for _, ref := range syn.Spec.Refs {
			c.mgr.GetLogger().Info(fmt.Sprintf("TODO syncing ref %s - %s/%s/%s", syn.Name, ref.Resource.Group, ref.Resource.Version, ref.Resource.Kind))
			ref := ref
			synthsByRef[ref.Resource] = struct{}{}

			current := c.refControllers[ref.Resource]
			if current != nil {
				c.mgr.GetLogger().Info("TODO skipping running")
				continue // already running
			}

			rc, err := NewKindWatchController(ctx, c, &ref.Resource)
			if err != nil {
				c.mgr.GetLogger().Info("TODO ERROR")
				return ctrl.Result{}, err
			}
			c.refControllers[ref.Resource] = rc
			c.mgr.GetLogger().Info("TODO started, requeu")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// Stop controllers that are no longer needed
	for ref, rc := range c.refControllers {
		c.mgr.GetLogger().Info(fmt.Sprintf("TODO rev syncing ref %s/%s/%s", ref.Group, ref.Version, ref.Kind))
		if _, ok := synthsByRef[ref]; ok {
			c.mgr.GetLogger().Info("TODO skipping deletion")
			continue
		}

		rc.Stop(ctx)
		delete(c.refControllers, ref)
		c.mgr.GetLogger().Info("TODO removed, requeue")
		return ctrl.Result{Requeue: true}, nil
	}

	c.mgr.GetLogger().Info("TODO nothing to do")
	return ctrl.Result{}, nil
}
