package liveness

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// namespaceController is responsible for progressing symphony deletion when its namespace is forcibly deleted.
// This can happen if clients get tricky with the /finalize API.
// Without this controller Eno resources will never be deleted since updates to remove the finalizers will fail.
type namespaceController struct {
	client client.Client
}

func NewNamespaceController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		Watches(&apiv1.Symphony{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: o.GetNamespace()}}}
		})).
		WithLogConstructor(manager.NewLogConstructor(mgr, "namespaceController")).
		Complete(&namespaceController{
			client: mgr.GetClient(),
		})
}

func (c *namespaceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ns := &corev1.Namespace{}
	err := c.client.Get(ctx, req.NamespacedName, ns)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting namespace: %w", err)
	}

	const annoKey = "eno.azure.io/recreation-reason"
	const annoValue = "OrphanedResources"

	// Delete the recreated namespace immediately.
	// Its finalizers will keep it around until we've had time to remove our finalizers.
	logger := logr.FromContextOrDiscard(ctx).WithValues("symphonyNamespace", ns.Name)
	if ns.Annotations != nil && ns.Annotations[annoKey] == annoValue {
		if ns.DeletionTimestamp != nil {
			return ctrl.Result{}, c.cleanup(ctx, req.Name)
		}
		err := c.client.Delete(ctx, ns)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting namespace: %w", err)
		}
		logger.V(0).Info("deleting recreated namespace")
		return ctrl.Result{}, nil
	}
	if err == nil { // important that this is the GET error
		return ctrl.Result{}, nil // namespace exists, nothing to do
	}

	// Nothing to do if the namespace doesn't have any symphonies
	list := &apiv1.SymphonyList{}
	err = c.client.List(ctx, list, client.InNamespace(req.Name))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing symphonies: %w", err)
	}
	if len(list.Items) == 0 {
		return ctrl.Result{}, nil // no orphaned resources, nothing to do
	}
	if time.Since(mostRecentCreation(list)) < time.Second {
		return ctrl.Result{RequeueAfter: time.Second}, nil // namespace probably just hasn't hit the cache yet
	}

	// Recreate the namespace briefly so we can remove the finalizers.
	// Any updates (including finalizer updates) will fail if the namespace doesn't exist.
	ns.Name = req.Name
	ns.Annotations = map[string]string{annoKey: annoValue}
	err = c.client.Create(ctx, ns)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("creating namespace: %w", err)
	}
	logger.V(0).Info("recreating missing namespace to free orphaned symphony")
	return ctrl.Result{}, nil
}

const removeFinalizersPatch = `[{ "op": "remove", "path": "/metadata/finalizers" }]`

func (c *namespaceController) cleanup(ctx context.Context, ns string) error {
	logger := logr.FromContextOrDiscard(ctx).WithValues("symphonyNamespace", ns)
	logger.V(0).Info("deleting any remaining symphonies in orphaned namespace")
	err := c.client.DeleteAllOf(ctx, &apiv1.Symphony{}, client.InNamespace(ns))
	if err != nil {
		return fmt.Errorf("deleting symphonies: %w", err)
	}

	list := &apiv1.ResourceSliceList{}
	err = c.client.List(ctx, list, client.InNamespace(ns))
	if err != nil {
		return fmt.Errorf("listing resource slices: %w", err)
	}

	for _, item := range list.Items {
		if len(item.Finalizers) == 0 {
			continue
		}
		err = c.client.Patch(ctx, &item, client.RawPatch(types.JSONPatchType, []byte(removeFinalizersPatch)))
		if err != nil {
			return fmt.Errorf("removing finalizers from resource slice %q: %w", item.Name, err)
		}
		logger := logger.WithValues("resourceSliceName", item.Name)
		logger.V(0).Info("forcibly removed finalizers")
	}

	return nil
}

func mostRecentCreation(list *apiv1.SymphonyList) time.Time {
	var max time.Time
	for _, item := range list.Items {
		if item.CreationTimestamp.After(max) {
			max = item.CreationTimestamp.Time
		}
	}
	return max
}
