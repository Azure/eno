package resourceslice

import (
	"context"
	"fmt"
	"slices"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type cleanupController struct {
	client        client.Client
	noCacheReader client.Reader
}

func NewCleanupController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.ResourceSlice{}).
		Watches(&apiv1.Composition{}, manager.NewCompositionToResourceSliceHandler(mgr.GetClient())). // TODO: Filter, avoid index?
		WithLogConstructor(manager.NewLogConstructor(mgr, "sliceCleanupController")).
		Complete(&cleanupController{
			client:        mgr.GetClient(),
			noCacheReader: mgr.GetAPIReader(),
		})
}

func (c *cleanupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("resourceSliceName", req.Name, "resourceSliceNamespace", req.Namespace)

	slice := &apiv1.ResourceSlice{}
	err := c.client.Get(ctx, req.NamespacedName, slice)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
	}
	logger = logger.WithValues("synthesisUUID", slice.Spec.SynthesisUUID)

	owner := metav1.GetControllerOf(slice)
	if owner != nil {
		logger = logger.WithValues("compositionName", owner.Name, "compositionNamespace", req.Namespace)
	}

	// Remove any old finalizers - resource slices don't use them any more
	if controllerutil.RemoveFinalizer(slice, "eno.azure.io/cleanup") {
		// TODO: test
		if err := c.client.Update(ctx, slice); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
		logger.V(0).Info("removed old resource slice finalizer")
		return ctrl.Result{}, nil
	}

	// Don't bother checking on brand new resource slices
	if delta := time.Since(slice.CreationTimestamp.Time); delta < 5*time.Second {
		// TODO: test
		return ctrl.Result{RequeueAfter: delta}, nil
	}

	del, err := c.shouldDelete(ctx, c.client, slice, owner)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking if resource slice should be deleted (cached): %w", err)
	}
	if !del {
		return ctrl.Result{}, nil // fail safe for stale cache
	}

	del, err = c.shouldDelete(ctx, c.noCacheReader, slice, owner)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("checking if resource slice should be deleted: %w", err)
	}
	if !del {
		return ctrl.Result{}, nil
	}

	if err := c.client.Delete(ctx, slice, &client.Preconditions{UID: &slice.UID}); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting resource slice: %w", err)
	}
	logger.V(0).Info("deleted unused resource slice", "age", time.Since(slice.CreationTimestamp.Time).Milliseconds())

	return ctrl.Result{}, nil
}

func (c *cleanupController) shouldDelete(ctx context.Context, reader client.Reader, slice *apiv1.ResourceSlice, ref *metav1.OwnerReference) (bool, error) {
	comp := &apiv1.Composition{}
	err := reader.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: slice.Namespace}, comp)
	if errors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("getting composition: %w", err)
	}

	// Don't delete slices that are part of an active synthesis
	if comp.Status.InFlightSynthesis != nil && comp.Status.InFlightSynthesis.UUID == slice.Spec.SynthesisUUID {
		return false, nil
	}

	// Check resource slice references
	for _, syn := range []*apiv1.Synthesis{comp.Status.CurrentSynthesis, comp.Status.PreviousSynthesis} {
		if syn == nil {
			continue
		}
		idx := slices.IndexFunc(syn.ResourceSlices, func(ref *apiv1.ResourceSliceRef) bool {
			return ref.Name == slice.Name
		})
		if idx != -1 {
			return false, nil
		}
	}

	return true, nil
}
