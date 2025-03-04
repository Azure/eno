package selfhealing

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
)

// sliceController check if the resource slice is deleted but it is still present in the composition current synthesis status.
// If yes, it will update the composition PendingResynthesis status to trigger re-synthesis process.
type sliceController struct {
	client                 client.Client
	noCacheReader          client.Reader
	selfHealingGracePeriod time.Duration
}

func NewSliceController(mgr ctrl.Manager, selfHealingGracePeriod time.Duration) error {
	s := &sliceController{
		client:                 mgr.GetClient(),
		noCacheReader:          mgr.GetAPIReader(),
		selfHealingGracePeriod: selfHealingGracePeriod,
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("selfHealingSliceController").
		Watches(&apiv1.Composition{}, newCompositionHandler()).
		Watches(&apiv1.ResourceSlice{}, newSliceHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "selfHealingSliceController")).
		Complete(s)
}

func (s *sliceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := s.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting composition: %w", err))
	}

	syn := &apiv1.Synthesizer{}
	syn.Name = comp.Spec.Synthesizer.Name
	err = s.client.Get(ctx, client.ObjectKeyFromObject(syn), syn)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("gettting synthesizer: %w", err))
	}

	logger = logger.WithValues("compositionGeneration", comp.Generation,
		"compositionName", comp.Name,
		"compositionNamespace", comp.Namespace,
		"synthesisID", comp.Status.GetCurrentSynthesisUUID())

	// Skip if the composition is not eligible for resynthesis, and check the synthesis result later
	if notEligibleForResynthesis(comp) {
		logger.V(1).Info("not eligible for resynthesis when checking the missing resource slice")
		// Use default grace period if the time since last synthesized is exceeds than the grace period
		if comp.Status.CurrentSynthesis == nil ||
			comp.Status.CurrentSynthesis.Synthesized == nil ||
			(s.selfHealingGracePeriod-time.Since(comp.Status.CurrentSynthesis.Synthesized.Time)) <= 0 {
			return ctrl.Result{Requeue: true, RequeueAfter: s.selfHealingGracePeriod}, nil
		}

		// Use the remaining grace period if the time since the last synthesized is less than the grace period
		return ctrl.Result{Requeue: true, RequeueAfter: s.selfHealingGracePeriod - time.Since(comp.Status.CurrentSynthesis.Synthesized.Time)}, nil
	}

	// Check if any resource slice referenced by the composition is deleted.
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := s.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			// Ensure the resource slice is missing by checking the resource from api-server
			isMissing, err := s.isSliceMissing(ctx, slice)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !isMissing {
				continue
			}

			// The resource slice should not be deleted if it is still referenced by the composition.
			// Update the composition status to trigger re-synthesis process.
			logger.V(1).Info("found missing resource slice and start resynthesis", "compositionName", comp.Name, "resourceSliceName", ref.Name)
			comp.ForceResynthesis()
			err = s.client.Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating composition pending resynthesis: %w", err)
			}
			return ctrl.Result{}, nil
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting resource slice: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

func (s *sliceController) isSliceMissing(ctx context.Context, slice *apiv1.ResourceSlice) (bool, error) {
	err := s.noCacheReader.Get(ctx, client.ObjectKeyFromObject(slice), slice)
	if errors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting resource slice from non cache reader: %w", err)
	}

	return false, nil
}

// Compositions aren't eligible to trigger resynthesis when:
// - They haven't ever been synthesized (they'll use the latest inputs anyway)
// - They are currently being synthesized or deleted
// - They are already pending resynthesis
//
// Composition should be resynthesized when the referenced resource slice is deleted
func notEligibleForResynthesis(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis == nil ||
		comp.Status.PendingSynthesis != nil ||
		comp.DeletionTimestamp != nil ||
		comp.ShouldIgnoreSideEffects() ||
		comp.ShouldForceResynthesis()
}

func newCompositionHandler() handler.EventHandler {
	apply := func(ctx context.Context, rli workqueue.TypedRateLimitingInterface[reconcile.Request], obj client.Object) {
		comp, ok := obj.(*apiv1.Composition)
		if !ok {
			logr.FromContextOrDiscard(ctx).V(0).Info("unexpected type given to newCompositionHandler")
			return
		}
		if comp.ShouldIgnoreSideEffects() {
			logr.FromContextOrDiscard(ctx).V(0).Info("skip missing resource slice check for composition that is ignoring side effects", "compositionName", comp.Name)
			return
		}

		rli.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: comp.Namespace, Name: comp.Name}})
	}
	return &handler.Funcs{
		CreateFunc: func(ctx context.Context, ce event.TypedCreateEvent[client.Object], rli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// No need to handle composition creation event for now
		},
		UpdateFunc: func(ctx context.Context, ue event.TypedUpdateEvent[client.Object], rli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// Check the updated composition only
			apply(ctx, rli, ue.ObjectNew)
		},
		DeleteFunc: func(ctx context.Context, de event.TypedDeleteEvent[client.Object], rli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// No need to handle composition deletion event for now
		},
	}
}

func newSliceHandler() handler.EventHandler {
	apply := func(rli workqueue.TypedRateLimitingInterface[reconcile.Request], obj client.Object) {
		owner := metav1.GetControllerOf(obj)
		if owner == nil {
			// No need to check the deleted resource slice which doesn't have an owner
			return
		}
		// Pass the composition name to the request to check missing resource slice
		rli.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      owner.Name,
				Namespace: obj.GetNamespace(),
			},
		})
	}

	return &handler.Funcs{
		CreateFunc: func(ctx context.Context, ce event.TypedCreateEvent[client.Object], rli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// No need to hanlde creation event for now
		},
		UpdateFunc: func(ctx context.Context, ue event.TypedUpdateEvent[client.Object], rli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			// No need to handle update event for now
		},
		DeleteFunc: func(ctx context.Context, de event.TypedDeleteEvent[client.Object], rli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			apply(rli, de.Object)
		},
	}
}
