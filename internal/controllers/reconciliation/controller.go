package reconciliation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/conf"
)

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
	logger := c.logger.WithValues("generatedResourceName", gr.Name, "generatedResourceGeneration", gr.Generation)

	// TODO: Should we have a per-resource cooldown period to debounce frequent updates?

	// Delete
	if gr.DeletionTimestamp != nil {
		cmd := exec.CommandContext(ctx, "kubectl", "delete", "-f=-")
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		cmd.Stdin = bytes.NewBufferString(gr.Spec.Manifest)
		cmd.Env = []string{fmt.Sprintf("KUBECONFIG=/etc/upstream-kubeconfig")}
		if err := cmd.Run(); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting resource: %w", err)
		}
		if !controllerutil.RemoveFinalizer(gr, finalizerName) {
			return ctrl.Result{}, nil // done - just wait for resource deletion
		}
		if err := c.client.Update(ctx, gr); err != nil {
			return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
		}
		logger.Info("deleted resource")
		return ctrl.Result{Requeue: true}, nil
	}

	if controllerutil.AddFinalizer(gr, finalizerName) {
		if err := c.client.Update(ctx, gr); err != nil {
			return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		logger.Info("added finalizer")
	}

	// Create/update
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f=-")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	cmd.Stdin = bytes.NewBufferString(gr.Spec.Manifest)
	cmd.Env = []string{fmt.Sprintf("KUBECONFIG=/etc/upstream-kubeconfig")}
	if err := cmd.Run(); err != nil {
		return ctrl.Result{}, fmt.Errorf("applying resource: %w", err)
	}
	logger.Info("wrote resource")

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
		return ctrl.Result{}, c.client.Status().Update(ctx, gr)
	}

	result := ctrl.Result{}
	if gr.Spec.ReconcileInterval != nil {
		result.RequeueAfter = gr.Spec.ReconcileInterval.Duration
	}
	return result, nil
}
