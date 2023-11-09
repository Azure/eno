package synthesis

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
)

type Config struct {
	WrapperImage    string
	JobSA           string
	MaxRestarts     int32
	Timeout         time.Duration
	RolloutCooldown time.Duration
}

func NewController(mgr ctrl.Manager, cfg *Config) error {
	plc := &podLifecycleController{
		config: cfg,
		client: mgr.GetClient(),
	}
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Watches(&apiv1.Synthesizer{}, &synthEventHandler{ctrl: plc}).
		Owns(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "podLifecycleController")).
		Build(plc)
	if err != nil {
		return err
	}

	// TODO: Separate constructors?

	sc := &statusController{
		config: cfg,
		client: mgr.GetClient(),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "statusController")).
		Build(sc)
	return err
}
