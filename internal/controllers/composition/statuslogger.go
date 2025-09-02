package composition

import (
	"context"
	"math/rand/v2"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type statusLogger struct {
	client    client.Client
	frequency time.Duration
	logFn     func(ctx context.Context, msg string, args ...any)
}

func NewStatusLogger(mgr ctrl.Manager, freq time.Duration) error {
	c := &statusLogger{
		client:    mgr.GetClient(),
		frequency: freq,
		logFn: func(ctx context.Context, msg string, args ...any) {
			logr.FromContextOrDiscard(ctx).V(0).Info(msg, args...)
		},
	}
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.TypedOptions[reconcile.Request]{
			// Hardcoded safety limit to avoid spewing too many logs
			RateLimiter: &workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Every(time.Second), 50)},
		}).
		For(&apiv1.Composition{}, builder.WithPredicates(c.newPredicate())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "compositionStatusLogger")).
		Complete(c)
}

func (c *statusLogger) newPredicate() predicate.Predicate {
	return &predicate.Funcs{
		CreateFunc:  func(tce event.TypedCreateEvent[client.Object]) bool { return true },
		DeleteFunc:  func(tde event.TypedDeleteEvent[client.Object]) bool { return true },
		GenericFunc: func(tge event.TypedGenericEvent[client.Object]) bool { return false },
		UpdateFunc: func(tue event.TypedUpdateEvent[client.Object]) bool {
			compA, okA := tue.ObjectNew.(*apiv1.Composition)
			compB, okB := tue.ObjectOld.(*apiv1.Composition)
			return okA && okB && !reflect.DeepEqual(compA.Status.Simplified, compB.Status.Simplified)
		},
	}
}

func (c *statusLogger) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if client.IgnoreNotFound(err) != nil || comp.Status.Simplified == nil {
		return ctrl.Result{}, err
	}

	fields := []any{
		"compositionName", comp.Name,
		"compositionNamespace", comp.Namespace,
		"compositionGeneration", comp.Generation,
		"status", comp.Status.Simplified.Status,
		"error", comp.Status.Simplified.Error,
	}
	if syn := comp.Status.CurrentSynthesis; syn != nil {
		fields = append(fields,
			"currentSynthesisUUID", syn.UUID,
			"currentSynthesizerGeneration", syn.ObservedSynthesizerGeneration,
		)
	}
	if syn := comp.Status.InFlightSynthesis; syn != nil {
		fields = append(fields,
			"inflightSynthesisUUID", syn.UUID,
			"inflightSynthesizerGeneration", syn.ObservedSynthesizerGeneration,
		)
	}

	c.logFn(ctx, "current composition status", fields...)

	jitter := time.Duration(float64(c.frequency) * 0.2 * (0.5 - rand.Float64())) // 20% jitter
	return ctrl.Result{RequeueAfter: c.frequency + jitter}, nil
}
