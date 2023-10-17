package readiness

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/clientmgr"
	"github.com/Azure/eno/internal/conf"
)

type Controller struct {
	config    *conf.Config
	client    client.Client
	clientMgr *clientmgr.Manager[*apiv1.SecretKeyRef]
	logger    logr.Logger
}

func NewController(mgr ctrl.Manager, cmgr *clientmgr.Manager[*apiv1.SecretKeyRef], config *conf.Config) error {
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

	cond := meta.FindStatusCondition(gr.Status.Conditions, apiv1.ReadyConditionType)
	if cond != nil && time.Since(cond.LastTransitionTime.Time) < time.Second {
		return c.loop() // provide stable upper bound on polling rate
	}

	res := &unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(gr.Spec.Manifest), res); err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing resource manifest as json: %w", err)
	}

	cli, err := c.clientMgr.GetClient(ctx, gr.Spec.KubeConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = cli.Get(ctx, client.ObjectKeyFromObject(res), res)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
	}

	// Evaluate
	kind, ok, err := unstructured.NestedString(res.Object, "kind")
	if !ok || err != nil {
		return ctrl.Result{}, nil
	}

	cond = &metav1.Condition{
		Type:               apiv1.ReadyConditionType,
		ObservedGeneration: gr.Generation,
		LastTransitionTime: metav1.Now(),
	}
	status, ok := c.evaluate(res, kind)
	if !ok {
		return ctrl.Result{}, nil
	}
	if status {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Ready"
		cond.Message = "Resource is ready"
	} else {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "NotReady"
		cond.Message = "Resource is not ready"
	}

	// Updates
	c.logger.Info("generated resource readiness state transition", "state", cond.Status, "generatedResourceName", gr.Name, "generatedResourceGeneration", gr.Generation)
	meta.SetStatusCondition(&gr.Status.Conditions, *cond)
	if err := c.client.Status().Update(ctx, gr); err != nil {
		return ctrl.Result{}, err
	}

	return c.loop()
}

func (c *Controller) loop() (ctrl.Result, error) {
	return ctrl.Result{RequeueAfter: c.config.StatusPollingInterval}, nil
}

func (c *Controller) evaluate(obj *unstructured.Unstructured, kind string) (bool, bool) {
	switch kind {
	case "Deployment":
		deploy := &appsv1.Deployment{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, deploy); err != nil {
			return false, false
		}
		return c.evaluateDeployment(deploy), true

	default:
		return false, false
	}
}

func (c *Controller) evaluateDeployment(deploy *appsv1.Deployment) bool {
	return deploy.Generation == deploy.Status.ObservedGeneration &&
		deploy.Spec.Replicas != nil &&
		*deploy.Spec.Replicas == deploy.Status.ReadyReplicas
}
