package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/conf"
)

// TODO: Implement another controller to get the status of reconciled resources and reflect back into the GeneratedResource resources

// TODO: Will the client re-discover new types after creating CRDs?

const finalizerName = "eno.azure.io/cleanup"

type Controller struct {
	config *conf.Config
	client client.Client
	logger logr.Logger
}

func NewController(mgr ctrl.Manager, config *conf.Config) error {
	c := &Controller{
		config: config,
		client: mgr.GetClient(),
		logger: mgr.GetLogger(),
	}

	_, err := ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.GeneratedResource{}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	gr := &apiv1.GeneratedResource{}
	err := c.client.Get(ctx, req.NamespacedName, gr)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting generated resource: %w", err))
	}

	if controllerutil.AddFinalizer(gr, finalizerName) {
		if err := c.client.Update(ctx, gr); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		c.logger.Info("added finalizer")
	}

	res := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(gr.Spec.Manifest), res); err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing resource manifest as json: %w", err)
	}

	cli := c.client // TODO: Support external clients

	current := res.DeepCopy()
	err = cli.Get(ctx, client.ObjectKeyFromObject(res), current)
	if errors.IsNotFound(err) {
		// Remove finalizer
		if gr.DeletionTimestamp != nil {
			if !controllerutil.RemoveFinalizer(gr, finalizerName) {
				return ctrl.Result{}, nil // done - just wait for resource deletion
			}
			if err := c.client.Update(ctx, gr); err != nil {
				return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
			c.logger.Info("removed finalizer")
			return ctrl.Result{}, nil
		}

		// Create
		if err := cli.Create(ctx, res); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating resource: %w", err)
		}
		c.logger.Info("created resource")
		return ctrl.Result{Requeue: true}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	// Delete
	if gr.DeletionTimestamp != nil {
		err = cli.Delete(ctx, res)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting resource: %w", err)
		}
		c.logger.Info("deleted resource")
		return ctrl.Result{Requeue: true}, nil
	}

	// Update
	if !deepCompare(current, res) {
		err = cli.Update(ctx, res)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating resource: %w", err)
		}
		c.logger.Info("updated resource")
		return ctrl.Result{Requeue: true}, nil
	}

	// Reflect status back to CR
	cond := meta.FindStatusCondition(gr.Status.Conditions, apiv1.ReconciledConditionType)
	if cond == nil || cond.ObservedGeneration != gr.Generation {
		meta.SetStatusCondition(&gr.Status.Conditions, metav1.Condition{
			Type:               apiv1.ReconciledConditionType,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gr.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             "Synced",
			Message:            "Resource is in sync",
		})
		return ctrl.Result{}, cli.Status().Update(ctx, gr)
	}

	result := ctrl.Result{Requeue: true}
	if gr.Spec.ReconcileInterval != nil {
		result.RequeueAfter = gr.Spec.ReconcileInterval.Duration
	}
	return result, nil
}

func deepCompare(current, next *unstructured.Unstructured) bool {
	// some resources like configmaps have a data property instead of spec
	var a, b any
	if next.Object["data"] != nil {
		a = current.Object["data"]
		b = next.Object["data"]
	} else {
		a = current.Object["spec"]
		b = next.Object["spec"]
	}

	// TODO: Consider using the k8s internal comparison semantics to avoid removing fields written by other components
	return current.GetDeletionTimestamp() == nil &&
		reflect.DeepEqual(a, b) &&
		reflect.DeepEqual(current.GetLabels(), next.GetLabels()) &&
		reflect.DeepEqual(current.GetAnnotations(), next.GetAnnotations()) &&
		reflect.DeepEqual(current.GetOwnerReferences(), next.GetOwnerReferences())
}
