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

type compToSynHandler struct {
	client client.Client
}

func enqueueSynthesizerFromCompositions(client client.Client) *compToSynHandler {
	return &compToSynHandler{client: client}
}

func (h *compToSynHandler) Generic(ctx context.Context, evt event.GenericEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *compToSynHandler) Create(ctx context.Context, evt event.CreateEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *compToSynHandler) Delete(ctx context.Context, evt event.DeleteEvent, q workqueue.RateLimitingInterface) {
	h.handle(ctx, evt.Object, q)
}

func (h *compToSynHandler) Update(ctx context.Context, evt event.UpdateEvent, q workqueue.RateLimitingInterface) {
	switch {
	case evt.ObjectNew != nil:
		h.handle(ctx, evt.ObjectNew, q)
	case evt.ObjectOld != nil:
		h.handle(ctx, evt.ObjectOld, q)
	default:
	}
}

func (h *compToSynHandler) handle(ctx context.Context, obj client.Object, q workqueue.RateLimitingInterface) {
	comp, ok := obj.(*apiv1.Composition)
	if obj == nil || !ok || comp.Spec.Synthesizer.Name == "" {
		return
	}

	q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
		Name: comp.Spec.Synthesizer.Name,
	}})
}
