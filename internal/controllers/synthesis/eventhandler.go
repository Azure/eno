package synthesis

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
)

type synthEventHandler struct {
	ctrl *Controller
}

func (h *synthEventHandler) Generic(ctx context.Context, evt event.GenericEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *synthEventHandler) Create(ctx context.Context, evt event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *synthEventHandler) Delete(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *synthEventHandler) Update(ctx context.Context, evt event.UpdateEvent, q workqueue.RateLimitingInterface) {
	switch {
	case evt.ObjectNew != nil:
		h.handle(ctx, evt.ObjectNew, q)
	case evt.ObjectOld != nil:
		h.handle(ctx, evt.ObjectOld, q)
	default:
	}
}

func (h *synthEventHandler) handle(ctx context.Context, obj client.Object, q workqueue.RateLimitingInterface) {
	if obj == nil {
		h.ctrl.logger.Info("synthHandler got nil object")
		return
	}

	// TODO: Randomize list order?

	list := &apiv1.CompositionList{}
	err := h.ctrl.client.List(ctx, list)
	if err != nil {
		// this should be impossible since we're reading from the informer cache
		h.ctrl.logger.Error(err, "error while listing compositions to be enqueued")
		return
	}
	// TODO: Index
	for _, item := range list.Items {
		if item.Spec.Synthesizer.Name != "" && item.Spec.Synthesizer.Name == obj.GetName() {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			}})
		}
	}
}
