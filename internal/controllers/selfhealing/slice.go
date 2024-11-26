package selfhealing

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
)

const (
	PodTimeout = time.Minute * 2
)

// sliceController check if the resource slice is deleted but it is still present in the composition current synthesis status.
// If yes, it will update the composition PendingResynthesis status to trigger re-synthesis process.
type sliceController struct {
	client client.Client
}

func NewSliceController(mgr ctrl.Manager) error {
	s := &sliceController{
		client: mgr.GetClient(),
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("selfHealingSliceController").
		Watches(&apiv1.ResourceSlice{}, newSliceHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "selfHealingSliceController")).
		Complete(s)
}

func (s *sliceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := s.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
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
		// Use default pod timeout
		if syn.Spec.PodTimeout == nil {
			return ctrl.Result{Requeue: true, RequeueAfter: PodTimeout}, nil
		}
		return ctrl.Result{Requeue: true, RequeueAfter: syn.Spec.PodTimeout.Duration}, nil
	}

	// Check if any resource slice referenced by the composition is deleted.
	for _, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		slice := &apiv1.ResourceSlice{}
		slice.Name = ref.Name
		slice.Namespace = comp.Namespace
		err := s.client.Get(ctx, client.ObjectKeyFromObject(slice), slice)
		if errors.IsNotFound(err) {
			// The resource slice should not be deleted if it is still referenced by the composition.
			// Update the composition status to trigger re-synthesis process.
			logger.V(1).Info("found missing resource slice and start resynthesis", "compositionName", comp.Name, "resourceSliceName", ref.Name)
			comp.Status.PendingResynthesis = ptr.To(metav1.Now())
			err = s.client.Status().Update(ctx, comp)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("updating composition pending resynthesis: %w", err)
			}
			return ctrl.Result{}, nil
		}

		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
		}
	}

	return ctrl.Result{}, nil
}

// Compositions aren't eligible to receive an updated synthesizer when:
// - They haven't ever been synthesized (they'll use the latest inputs anyway)
// - They are currently being synthesized or deleted
// - They are already pending resynthesis
//
// Composition should be resynthesized when the referenced resource slice is deleted even
// the composition should ignore side effect.
func notEligibleForResynthesis(comp *apiv1.Composition) bool {
	return comp.Status.CurrentSynthesis == nil ||
		comp.Status.CurrentSynthesis.Synthesized == nil ||
		comp.DeletionTimestamp != nil ||
		comp.Status.PendingResynthesis != nil
}

func newSliceHandler() handler.EventHandler {
	apply := func(rli workqueue.RateLimitingInterface, obj client.Object) {
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
		CreateFunc: func(ctx context.Context, ce event.CreateEvent, rli workqueue.RateLimitingInterface) {
			// No need to hanlde creation event for now
		},
		UpdateFunc: func(ctx context.Context, ue event.UpdateEvent, rli workqueue.RateLimitingInterface) {
			// No need to handle update event for now
		},
		DeleteFunc: func(ctx context.Context, de event.DeleteEvent, rli workqueue.RateLimitingInterface) {
			apply(rli, de.Object)
		},
	}
}
