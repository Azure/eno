package liveness

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var orphanableKinds = []string{"Symphony", "Composition", "ResourceSlice"}

// namespaceController is responsible for progressing resource deletion when the namespace is forcibly deleted.
// This can happen if clients get tricky with the /finalize API.
// Without this controller Eno resources will never be deleted since updates to remove the finalizers will fail.
type namespaceController struct {
	client                client.Client
	creationGracePeriod   time.Duration
	orphanCheckIterations int
}

func NewNamespaceController(mgr ctrl.Manager, checks int, creationGracePeriod time.Duration) error {
	b := ctrl.NewControllerManagedBy(mgr).For(&corev1.Namespace{})

	for _, kind := range orphanableKinds {
		b = b.Watches(&metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{
				Kind:       kind,
				APIVersion: apiv1.SchemeGroupVersion.String(),
			},
		}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: o.GetNamespace()}}}
		}))
	}

	return b.WithLogConstructor(manager.NewLogConstructor(mgr, "namespaceLivenessController")).
		Complete(&namespaceController{
			client:                mgr.GetClient(),
			creationGracePeriod:   creationGracePeriod,
			orphanCheckIterations: checks,
		})
}

func (c *namespaceController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	ns := &corev1.Namespace{}
	ns.Name = req.Name
	err := c.client.Get(ctx, req.NamespacedName, ns)
	if client.IgnoreNotFound(err) != nil {
		logger.Error(err, "failed to get namespace")
		return ctrl.Result{}, err
	}

	const annoKey = "eno.azure.io/recreation-reason"
	const annoValue = "OrphanedResources"

	// Delete the recreated namespace immediately.
	// Its finalizers will keep it around until we've had time to remove our finalizers.
	logger = logger.WithValues("resourceNamespace", ns.Name)
	ctx = logr.NewContext(ctx, logger)
	if ns.Annotations != nil && ns.Annotations[annoKey] == annoValue {
		if ns.DeletionTimestamp != nil {
			return ctrl.Result{}, c.cleanup(ctx, req.Name)
		}
		logger.Info("Sucessfully cleaned up orphaned eno resources")
		err := c.client.Delete(ctx, ns)
		if err != nil {
			logger.Error(err, "failed to delete namespace")
			return ctrl.Result{}, err
		}
		logger.Info("Deleting recreated namespace")
		return ctrl.Result{}, nil
	}
	if err == nil {
		logger.Info("namespace still exits, do nothing")
		// Successful GETs mean the namespace still exists - nothing for us to do
		return ctrl.Result{}, nil
	}

	// Avoid recreating the namespace when it doesn't have any orphaned resources
	for i := 1; true; i++ {
		var foundOrphans bool
		for _, kind := range orphanableKinds {
			hasOrphans, res, err := c.findOrphans(ctx, ns.Name, kind)
			if err != nil {
				logger.Error(err, "failed to find orphaned resources", "resourceKind", kind)
				return ctrl.Result{}, err
			}
			if res != nil {
				return *res, nil
			}
			if hasOrphans {
				foundOrphans = true
			}
		}
		if !foundOrphans {
			return ctrl.Result{}, nil
		}
		if i >= c.orphanCheckIterations {
			break
		}

		// Sleep a bit before the next check to let informers catch up.
		time.Sleep(time.Second / 2)
	}

	// Recreate the namespace briefly so we can remove the finalizers.
	// Any updates (including finalizer updates) will fail if the namespace doesn't exist.
	ns.Annotations = map[string]string{annoKey: annoValue}
	err = c.client.Create(ctx, ns)
	if err != nil {
		logger.Error(err, "failed to create namespace")
		return ctrl.Result{}, err
	}
	logger.Info("recreated missing namespace to free orphaned resources")
	return ctrl.Result{}, nil
}

func (c *namespaceController) cleanup(ctx context.Context, ns string) error {
	logger := logr.FromContextOrDiscard(ctx)

	logger.Info("deleting any remaining resource in orphaned namespace")
	for _, kind := range orphanableKinds {
		err := c.client.DeleteAllOf(ctx, &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{Kind: kind, APIVersion: apiv1.SchemeGroupVersion.String()},
		}, client.InNamespace(ns))
		if err != nil {
			return fmt.Errorf("deleting resources of kind %q: %w", kind, err)
		}
	}
	return nil
}

func (c *namespaceController) findOrphans(ctx context.Context, ns, kind string) (bool, *ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	list := &metav1.PartialObjectMetadataList{}
	list.Kind = kind
	list.APIVersion = "eno.azure.io/v1"
	err := c.client.List(ctx, list, client.InNamespace(ns))
	if err != nil {
		return false, nil, err
	}
	if len(list.Items) == 0 {
		return false, nil, nil // no orphaned resources, nothing to do
	}
	if delta := time.Since(mostRecentCreation(list)); delta < c.creationGracePeriod {
		logger.Info(fmt.Sprintf("refusing to free orphaned resources because one or more are too new. Requeing after[%d]", delta), "resourceKind", kind)
		return false, &ctrl.Result{RequeueAfter: delta}, nil // namespace probably just hasn't hit the cache yet
	}
	var names []string
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	logger.Info("found orphaned resources", "resourceKind", kind, "resourceCount", len(names), "names", names)
	return true, nil, nil
}

func mostRecentCreation(list *metav1.PartialObjectMetadataList) time.Time {
	var max time.Time
	for _, item := range list.Items {
		if item.CreationTimestamp.After(max) {
			max = item.CreationTimestamp.Time
		}
	}
	return max
}
