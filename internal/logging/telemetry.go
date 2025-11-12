package logging

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Logger provides common telemetry logging functionality
type Logger struct {
	logFn func(ctx context.Context, msg string, args ...any)
}

// NewLogger creates a new telemetry logger with default log function
func NewLogger() *Logger {
	return &Logger{
		logFn: func(ctx context.Context, msg string, args ...any) {
			logr.FromContextOrDiscard(ctx).V(0).Info(msg, args...)
		},
	}
}

// NewLoggerWithBuild creates a logger with serviceBuild field if buildVersion is provided
func NewLoggerWithBuild(zl *zap.Logger, buildVersion string) logr.Logger {
	logger := zapr.NewLogger(zl)

	// Add serviceBuild to all log entries if provided
	if buildVersion != "" {
		logger = logger.WithValues("serviceBuild", buildVersion)
	}

	return logger
}

func (l *Logger) Log(ctx context.Context, msg string, field ...any) {
	// Add timestamp to all log entries
	enrichedFields := []any{"timestamp", time.Now()}
	enrichedFields = append(enrichedFields, field...)
	l.logFn(ctx, msg, enrichedFields...)
}

func (l *Logger) WithLogFn(fn func(ctx context.Context, msg string, args ...any)) *Logger {
	l.logFn = fn
	return l
}

// Telemetry controller is a generic telemetry controller that can be used for any CR
type TelemetryController[T client.Object] struct {
	client    client.Client
	logger    *Logger
	frequency time.Duration
	template  T

	// Callbacks for customizatoin
	predicateFn     func() predicate.Predicate
	extractFieldsFn func(ctx context.Context, obj T) []any
	eventTypeFn     func(obj T) string
	messageFn       func() string
	controllerName  string
}

// TelemetryConfig configures a telemetry controller
type TelemetryConfig[T client.Object] struct {
	Manager         ctrl.Manager
	Frequency       time.Duration
	PredicateFn     func() predicate.Predicate
	ExtractFieldsFn func(ctx context.Context, obj T) []any
	EventTypeFn     func(obj T) string
	MessageFn       func() string
	ControllerName  string
	Logger          *Logger
}

// NewTelemetryController creates a generic telemetry controller
func NewTelemetryController[T client.Object](config TelemetryConfig[T], obj T) error {
	logger := config.Logger
	if logger == nil {
		logger = NewLogger()
	}
	c := &TelemetryController[T]{
		client:          config.Manager.GetClient(),
		logger:          logger,
		frequency:       config.Frequency,
		template:        obj,
		predicateFn:     config.PredicateFn,
		extractFieldsFn: config.ExtractFieldsFn,
		eventTypeFn:     config.EventTypeFn,
		messageFn:       config.MessageFn,
		controllerName:  config.ControllerName,
	}
	return ctrl.NewControllerManagedBy(config.Manager).
		WithOptions(controller.TypedOptions[reconcile.Request]{
			RateLimiter: &workqueue.TypedBucketRateLimiter[reconcile.Request]{
				Limiter: rate.NewLimiter(rate.Every(time.Second), 50),
			},
		}).
		For(obj, builder.WithPredicates(c.predicateFn())).
		WithLogConstructor(manager.NewLogConstructor(config.Manager, c.controllerName)).
		Complete(c)
}

func (c *TelemetryController[T]) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	// Create a new instance from the template
	obj := c.template.DeepCopyObject().(T)

	err := c.client.Get(ctx, req.NamespacedName, obj)
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Object was deleted - log with minimal info
			c.logger.Log(ctx, c.messageFn(),
				"name", req.NamespacedName.Name,
				"namespace", req.NamespacedName.Namespace,
				"eventType", "status_deleted")
		}
		return ctrl.Result{}, nil
	}

	// Extract fields and event type
	fields := c.extractFieldsFn(ctx, obj)
	eventType := c.eventTypeFn(obj)

	// add event type to fields
	fields = append([]any{"eventType", eventType}, fields...)
	c.logger.Log(ctx, c.messageFn(), fields...)

	if c.frequency > 0 {
		jitter := time.Duration(float64(c.frequency) * 0.2 * (0.5 - rand.Float64()))
		return ctrl.Result{RequeueAfter: c.frequency + jitter}, nil
	}

	return ctrl.Result{}, nil
}

// AddFields is a helper to build field arrays safely
func AddFields(base []any, keyValues ...any) []any {
	return append(base, keyValues...)
}
