package flowcontrol

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type synthesisConcurrencyLimiter struct {
	client   client.Client
	limit    int
	cooldown time.Duration
}

func NewSynthesisConcurrencyLimiter(mgr ctrl.Manager, limit int, cooldown time.Duration) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("synthesisConcurrencyLimiter").
		Watches(&apiv1.Composition{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "synthesisConcurrencyLimiter")).
		Complete(&synthesisConcurrencyLimiter{
			client:   mgr.GetClient(),
			limit:    limit,
			cooldown: cooldown,
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
	var pending []*apiv1.Composition
	for _, comp := range list.Items {
		comp := comp
		current := comp.Status.CurrentSynthesis
		if current == nil || current.Synthesized != nil {
			continue // not ready or already synthesized
		}
		if current.UUID == "" {
			pending = append(pending, &comp)
		} else {
			active++
		}
	}
	activeSyntheses.Add(float64(active))
	pendingSyntheses.Add(float64(len(pending)))

	if active >= c.limit {
		logger.V(1).Info("refusing to dispatch synthesis because concurrency limit has been reached", "active", active, "pending", pending)
		return ctrl.Result{}, nil
	}

	if len(pending) == 0 {
		return ctrl.Result{}, nil // nothing to dispatch
	}
	next := pending[rand.Intn(len(pending))]
	logger = logger.WithValues("compositionName", next.Name, "compositionNamespace", next.Namespace, "compositionGeneration", next.Generation)

	// Dispatch the next pending synthesis
	if next.Status.CurrentSynthesis == nil {
		next.Status.CurrentSynthesis = &apiv1.Synthesis{}
	}
	copy := next.DeepCopy()
	copy.Status.CurrentSynthesis.UUID = uuid.NewString()

	if err := c.client.Status().Patch(ctx, copy, client.MergeFrom(next)); err != nil {
		return ctrl.Result{}, fmt.Errorf("writing uuid to composition status: %w", err)
	}
	logger.V(0).Info("dispatched synthesis")

	return ctrl.Result{Requeue: true, RequeueAfter: c.cooldown}, nil
}
