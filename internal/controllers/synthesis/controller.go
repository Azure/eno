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

// IMPORTANT: The manager's pod informer should be filtered on a label present on pods created by this controller to avoid caching all pods on the cluster
func NewController(mgr ctrl.Manager, cfg *Config) error {
	pcc := &podCreationController{
		config: cfg,
		client: mgr.GetClient(),
	}
	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Watches(&apiv1.Synthesizer{}, &synthEventHandler{ctrl: pcc}).
		Owns(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "podCreationController")).
		Build(pcc)
	if err != nil {
		return err
	}

	// TODO: Separate constructors?

	plc := &podLifecycleController{
		config: cfg,
		client: mgr.GetClient(),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "podLifecycleController")).
		Build(plc)
	return err
}
