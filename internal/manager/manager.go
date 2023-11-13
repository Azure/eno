package manager

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
)

const (
	IdxSlicesByCompositionGeneration = ".metadata.ownerReferences.compositionGen" // see: NewSlicesByCompositionGenerationKey
)

func New(cfg *rest.Config, logger logr.Logger) (ctrl.Manager, error) {
	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Logger: logger,
	})
	if err != nil {
		return nil, err
	}

	err = apiv1.SchemeBuilder.AddToScheme(mgr.GetScheme())
	if err != nil {
		return nil, err
	}

	err = mgr.GetFieldIndexer().IndexField(context.Background(), &apiv1.ResourceSlice{}, IdxSlicesByCompositionGeneration, func(o client.Object) []string {
		slice := o.(*apiv1.ResourceSlice)
		owner := metav1.GetControllerOf(slice)
		if owner == nil || owner.Kind != "Composition" {
			return nil
		}
		return []string{NewSlicesByCompositionGenerationKey(owner.Name, slice.Spec.CompositionGeneration)}
	})
	if err != nil {
		return nil, err
	}

	return mgr, nil
}

func NewLogConstructor(mgr ctrl.Manager, controllerName string) func(*reconcile.Request) logr.Logger {
	return func(req *reconcile.Request) logr.Logger {
		l := mgr.GetLogger().WithValues("controller", controllerName)
		if req != nil {
			l.WithValues("requestName", req.Name, "requestNamespace", req.Namespace)
		}
		return l
	}
}

// NewSlicesByCompositionGenerationKey documents the key structure used by IdxSlicesByCompositionGeneration.
func NewSlicesByCompositionGenerationKey(compName string, compGeneration int64) string {
	// keys will not collide because k8s doesn't allow slashes in names
	return fmt.Sprintf("%s/%d", compName, compGeneration)
}
