package rollout

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/controllers/synthesis"
	"github.com/Azure/eno/internal/manager"
)

type controller struct {
	client   client.Client
	cooldown time.Duration
}

// NewController re-synthesizes compositions when their synthesizer has changed while honoring a cooldown period.
func NewController(mgr ctrl.Manager, cooldown time.Duration) error {
	c := &controller{
		client:   mgr.GetClient(),
		cooldown: cooldown,
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Synthesizer{}).
		Watches(&apiv1.Composition{}, newCompositionHandler()).
		// TODO: Filter some events?
		WithLogConstructor(manager.NewLogConstructor(mgr, "synthesizerRolloutController")).
		Complete(c)
}

func (c *controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	syn := &apiv1.Synthesizer{}
	err := c.client.Get(ctx, req.NamespacedName, syn)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting synthesizer: %w", err))
	}
	logger = logger.WithValues("synthesizerName", syn.Name, "synthesizerNamespace", syn.Namespace, "synthesizerGeneration", syn.Generation)

	compList := &apiv1.CompositionList{}
	err = c.client.List(ctx, compList, client.MatchingFields{
		manager.IdxCompositionsBySynthesizer: syn.Name,
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing compositions: %w", err)
	}

	var latestRollout time.Time
	for _, comp := range compList.Items {
		if comp.Status.CurrentSynthesis != nil && comp.Status.PreviousSynthesis != nil && comp.Status.CurrentSynthesis.Synthesized != nil && comp.Status.CurrentSynthesis.Synthesized.Time.After(latestRollout) {
			latestRollout = comp.Status.CurrentSynthesis.Synthesized.Time
		}
	}
	if delta := time.Since(latestRollout); delta < c.cooldown {
		return ctrl.Result{RequeueAfter: c.cooldown - delta}, nil
	}

	// randomize list to avoid always rolling out changes in the same order
	rand.Shuffle(len(compList.Items), func(i, j int) { compList.Items[i] = compList.Items[j] })

	for _, comp := range compList.Items {
		comp := comp
		logger := logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation)

		// Compositions aren't eligible to receive an updated synthesizer when:
		// - They haven't ever been synthesized (they'll use the latest inputs anyway)
		// - They are currently being synthesized or deleted
		// - They are already in sync with the latest inputs
		if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil || comp.DeletionTimestamp != nil || isInSync(&comp, syn) {
			continue
		}

		synthesis.SwapStates(&comp)
		err = c.client.Status().Update(ctx, &comp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("swapping compisition state: %w", err)
		}

		logger.V(1).Info("advancing rollout process")
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}

func isInSync(comp *apiv1.Composition, syn *apiv1.Synthesizer) bool {
	return comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration >= syn.Generation
}

func newCompositionHandler() handler.EventHandler {
	return &handler.Funcs{
		CreateFunc: func(ctx context.Context, ce event.CreateEvent, rli workqueue.RateLimitingInterface) {
			// No need to handle creation events since the status will always be nil.
		},
		DeleteFunc: func(ctx context.Context, de event.DeleteEvent, rli workqueue.RateLimitingInterface) {
			// We don't handle deletes on purpose, since a composition being deleted can only ever
			// result in the cooldown period being shortened i.e. we lose track of a more recent
			// rollout event.
			//
			// It's okay that this state can be lost, since it falls within the promised semantics
			// of this controller. But ideally we can avoid it when possible.
		},
		UpdateFunc: func(ctx context.Context, ue event.UpdateEvent, rli workqueue.RateLimitingInterface) {
			newComp, ok := ue.ObjectNew.(*apiv1.Composition)
			if !ok {
				logr.FromContextOrDiscard(ctx).V(0).Info("unexpected type given to newCompositionToSynthesizerHandler")
				return
			}

			oldComp, ok := ue.ObjectOld.(*apiv1.Composition)
			if !ok {
				rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: newComp.Spec.Synthesizer.Name}})
				return
			}

			// Nothing we care about has changed
			if oldComp.Spec.Synthesizer.Name == newComp.Spec.Synthesizer.Name &&
				oldComp.Status.CurrentSynthesis != nil && newComp.Status.CurrentSynthesis != nil &&
				oldComp.Status.CurrentSynthesis.UUID == newComp.Status.CurrentSynthesis.UUID {
				return
			}

			rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: newComp.Spec.Synthesizer.Name}})
		},
	}
}
