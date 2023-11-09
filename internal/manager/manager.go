package manager

import (
	"context"
	"fmt"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	apiv1 "github.com/Azure/eno/api/v1"
)

const (
	IdxCompositionsBySynthesizer     = ".spec.synthesizer"
	IdxPodsByComposition             = ".metadata.ownerReferences.composition"
	IdxSlicesByCompositionGeneration = ".metadata.ownerReferences.compositionGen"
)

// TODO: Filter pods
// TODO: Filter by namespace

func New(cfg *rest.Config) (ctrl.Manager, error) {
	zapLogger, err := zap.NewDevelopment()
	if err != nil {
		return nil, err
	}

	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Logger: zapr.NewLogger(zapLogger),
	})
	if err != nil {
		return nil, err
	}

	err = apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme())
	if err != nil {
		return nil, err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.Composition{}, IdxCompositionsBySynthesizer, func(o client.Object) []string {
		comp := o.(*apiv1.Composition)
		return []string{comp.Spec.Synthesizer.Name}
	})
	if err != nil {
		return nil, err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{}, IdxPodsByComposition, func(o client.Object) []string {
		pod := o.(*corev1.Pod)
		owner := metav1.GetControllerOf(pod)
		if owner == nil || owner.Kind != "Composition" {
			return nil
		}
		return []string{owner.Name}
	})
	if err != nil {
		return nil, err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, IdxSlicesByCompositionGeneration, func(o client.Object) []string {
		slice := o.(*apiv1.ResourceSlice)
		owner := metav1.GetControllerOf(slice)
		if owner == nil || owner.Kind != "Composition" {
			return nil
		}
		// keys will not collide because k8s doesn't allow slashes in names
		return []string{fmt.Sprintf("%s/%d", owner.Name, slice.Spec.CompositionGeneration)}
	})
	if err != nil {
		return nil, err
	}

	return mgr, nil
}
