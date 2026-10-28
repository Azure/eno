package watch

import (
	"context"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type pruningController struct {
	client client.Client
}

func (c *pruningController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesizerName", comp.Spec.Synthesizer.Name,
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)

	synth := &apiv1.Synthesizer{}
	synth.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(synth), synth)
	if errors.IsNotFound(err) {
		// Without the synthesizer we can't tell which refs are still declared, so don't prune anything
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get synthesizer")
		return ctrl.Result{}, err
	}

	kept := make([]apiv1.InputRevisions, 0, len(comp.Status.InputRevisions))
	var pruned []string
	for _, ir := range comp.Status.InputRevisions {
		if shouldKeepInputRevision(comp, synth, ir.Key) {
			kept = append(kept, ir)
			continue
		}
		pruned = append(pruned, ir.Key)
	}
	if len(pruned) == 0 {
		return ctrl.Result{}, nil
	}

	logger.Info("pruning input revisions - no longer declared by synthesizer or bound by composition", "keys", pruned, "synthesizerGeneration", synth.Generation)
	comp.Status.InputRevisions = kept
	err = c.client.Status().Update(ctx, comp)
	if err != nil {
		logger.Error(err, "failed to update composition status")
		return ctrl.Result{}, err
	}

	logger.Info("pruned old input revisions from composition status", "keys", pruned)
	return ctrl.Result{}, nil
}

// shouldKeepInputRevision reports whether the input revision for key should be kept. Both must be true:
//   - The synthesizer still has a ref with this key.
//   - The ref points to a resource, either by naming one itself or through a matching composition binding.
//
// A composition binding by itself doesn't count. It can stay behind after the ref is removed
// (e.g. when the binding comes from a Symphony).
func shouldKeepInputRevision(comp *apiv1.Composition, synth *apiv1.Synthesizer, key string) bool {
	for _, ref := range synth.Spec.Refs {
		if ref.Key != key {
			continue
		}
		if ref.Resource.Name != "" {
			return true // implicit binding
		}
		for _, b := range comp.Spec.Bindings {
			if b.Key == key {
				return true
			}
		}
		return false
	}
	return false
}

// newSynthEventHandler enqueues every composition that uses the synthesizer, so revisions for refs
// removed from the synthesizer are pruned without waiting for an unrelated composition event.
func (c *pruningController) newSynthEventHandler() handler.TypedEventHandler[*apiv1.Synthesizer, reconcile.Request] {
	fn := func(ctx context.Context, synth *apiv1.Synthesizer) []reconcile.Request {
		logger := logr.FromContextOrDiscard(ctx)

		list := &apiv1.CompositionList{}
		err := c.client.List(ctx, list, client.MatchingFields{
			manager.IdxCompositionsBySynthesizer: synth.Name,
		})
		if err != nil {
			logger.Error(err, "failed to list compositions for synthesizer", "synthesizerName", synth.Name)
			return nil
		}

		reqs := make([]reconcile.Request, 0, len(list.Items))
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
		return reqs
	}
	return handler.TypedEnqueueRequestsFromMapFunc(fn)
}

// synthRefsChangedPredicate limits synthesizer events to the ones that can leave stale input revisions:
//   - Create: a synthesizer created after its compositions; earlier prune passes skipped them.
//   - Update: only when spec.refs changed. Other changes (e.g. image) can't make a revision stale.
//
// Deletes are dropped because the pruner doesn't prune without a synthesizer.
func synthRefsChangedPredicate() predicate.TypedPredicate[*apiv1.Synthesizer] {
	return predicate.TypedFuncs[*apiv1.Synthesizer]{
		CreateFunc: func(e event.TypedCreateEvent[*apiv1.Synthesizer]) bool {
			return true
		},
		UpdateFunc: func(e event.TypedUpdateEvent[*apiv1.Synthesizer]) bool {
			return !equality.Semantic.DeepEqual(e.ObjectOld.Spec.Refs, e.ObjectNew.Spec.Refs)
		},
		DeleteFunc: func(e event.TypedDeleteEvent[*apiv1.Synthesizer]) bool {
			return false
		},
		GenericFunc: func(e event.TypedGenericEvent[*apiv1.Synthesizer]) bool {
			return false
		},
	}
}
