package synthesis

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
)

const compBySynIndex = ".spec.synthesizer"

const compByPodIndex = ".metadata.composition"

type Config struct {
	WrapperImage    string
	JobSA           string
	MaxRestarts     int32
	Timeout         time.Duration
	RolloutCooldown time.Duration
}

// IMPORTANT: The manager's pod informer should be filtered on a label present on pods created by this controller to avoid caching all pods on the cluster
func NewController(mgr ctrl.Manager, cfg *Config) error {
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Composition{}, compBySynIndex, func(o client.Object) []string {
		comp := o.(*apiv1.Composition)
		return []string{comp.Spec.Synthesizer.Name}
	})
	if err != nil {
		return err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, compByPodIndex, func(o client.Object) []string {
		pod := o.(*corev1.Pod)
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "Composition" {
			return nil
		}
		// keys will not collide because k8s doesn't allow slashes in names
		return []string{owner.Name}
	})
	if err != nil {
		return err
	}

	psc := &podSpawnController{
		config: cfg,
		client: mgr.GetClient(),
		logger: mgr.GetLogger(),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Composition{}).
		Watches(&apiv1.Synthesizer{}, &synthEventHandler{ctrl: psc}).
		Build(psc)
	if err != nil {
		return err
	}

	lc := &lifecycleController{
		config: cfg,
		client: mgr.GetClient(),
		logger: mgr.GetLogger(),
	}
	_, err = ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		Build(lc)
	return err
}
