package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/conf"
)

const finalizerName = "eno.azure.io/cleanup"

type Controller struct {
	config    *conf.Config
	client    client.Client
	clientMgr *clientmgr.Manager[string]
	logger    logr.Logger
}

func NewController(mgr ctrl.Manager, cmgr *clientmgr.Manager[string], config *conf.Config) error {
	c := &Controller{
		config:    config,
		client:    mgr.GetClient(),
		clientMgr: cmgr,
		logger:    mgr.GetLogger(),
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
	logger := c.logger.WithValues("generatedResourceName", gr.Name, "generatedResourceGeneration", gr.Generation)

	if controllerutil.AddFinalizer(gr, finalizerName) {
		if err := c.client.Update(ctx, gr); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		logger.Info("added finalizer")
	}

	res := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(gr.Spec.Manifest), res); err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing resource manifest as json: %w", err)
	}

	cli, err := c.clientMgr.GetClient(ctx, "")
	if err != nil {
		return ctrl.Result{}, err
	}

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
			logger.Info("removed finalizer")
			return ctrl.Result{}, nil
		}
		err = nil
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
		logger.Info("deleted resource")
		return ctrl.Result{Requeue: true}, nil
	}

	// Create/update
	// TODO: Avoid patching for every reconcile by writing the resource version to the GR status - only reconcile when the resource version of GR or reconciled resource have changed
	err = cli.Patch(ctx, res, client.Apply, client.FieldOwner("eno"), client.ForceOwnership)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating resource: %w", err)
	}
	if !equality.Semantic.DeepEqual(res, current) {
		logger.Info("wrote resource")
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

	result := ctrl.Result{}
	if gr.Spec.ReconcileInterval != nil {
		result.RequeueAfter = gr.Spec.ReconcileInterval.Duration
	}
	return result, nil
}
