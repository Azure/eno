package watch

import (
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
)

// NewController constructs all of the various control loops that implement the
// referenced resource watching functionality.
func NewController(mgr ctrl.Manager, writesPerSecond int) error {
	err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Watches(&apiv1.Synthesizer{}, manager.SynthToCompositionHandler(mgr.GetClient())).
		WithLogConstructor(manager.NewLogConstructor(mgr, "watchDiscoveryController")).
		Complete(&discoveryController{
			client: mgr.GetClient(),
		})
	if err != nil {
		return err
	}

	limiter := &workqueue.BucketRateLimiter{
		Limiter: rate.NewLimiter(rate.Every(time.Second), writesPerSecond),
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("watchControllerController").
		Watches(&apiv1.Synthesizer{}, manager.SingleEventHandler()).
		WithLogConstructor(manager.NewLogConstructor(mgr, "watchControllerController")).
		Complete(&controllerController{
			mgr:            mgr,
			client:         mgr.GetClient(),
			refControllers: make(map[apiv1.ResourceRef]*refStatusController),
			limiter:        limiter,
		})
}
