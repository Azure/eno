package flowcontrol

import (
	"context"
	"fmt"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type synthesisConcurrencyLimiter struct {
	client client.Client
	limit  int
}

func NewSynthesisConcurrencyLimiter(mgr ctrl.Manager, limit int) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("synthesisConcurrencyLimiter").
		Watches(&apiv1.Composition{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "synthesisConcurrencyLimiter")).
		Complete(&synthesisConcurrencyLimiter{
			client: mgr.GetClient(),
			limit:  limit,
		})
}

func (c *synthesisConcurrencyLimiter) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	list := &apiv1.CompositionList{}
	err := c.client.List(ctx, list)
	if err != nil {
		return ctrl.Result{}, err
	}

	var active int
	var pending int
	var next *apiv1.Composition
	for _, comp := range list.Items {
		comp := comp
		current := comp.Status.CurrentSynthesis
		if current == nil || current.Synthesized != nil {
			continue // not ready or already synthesized
		}
		if current.UUID == "" {
			pending++
			next = &comp
		} else {
			active++
		}
	}
	activeSyntheses.Add(float64(active))
	pendingSyntheses.Add(float64(pending))

	if active >= c.limit {
		logger.V(1).Info("refusing to dispatch synthesis because concurrency limit has been reached", "active", active, "pending", pending)
		return ctrl.Result{}, nil
	}

	if next == nil {
		return ctrl.Result{}, nil // nothing to dispatch
	}
	logger = logger.WithValues("compositionName", next.Name, "compositionNamespace", next.Namespace, "compositionGeneration", next.Generation)

	// Dispatch the next pending synthesis
	if next.Status.CurrentSynthesis == nil {
		next.Status.CurrentSynthesis = &apiv1.Synthesis{}
	}
	next.Status.CurrentSynthesis.UUID = uuid.NewString()

	if err := c.client.Status().Update(ctx, next); err != nil {
		return ctrl.Result{}, fmt.Errorf("writing uuid to composition status: %w", err)
	}
	logger.V(1).Info("dispatched synthesis")

	return ctrl.Result{}, nil
}
