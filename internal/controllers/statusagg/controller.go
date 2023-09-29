package statusagg

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/conf"
)

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
		For(&apiv1.Composition{}).
		Owns(&apiv1.GeneratedResource{}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}

	// TODO: Use an index to avoid listing all GRs
	grs := &apiv1.GeneratedResourceList{}
	err = c.client.List(ctx, grs)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("listing generated resources: %w", err))
	}

	// Condition construction
	cond := meta.FindStatusCondition(comp.Status.Conditions, apiv1.ReadyConditionType)
	if cond == nil {
		cond = &metav1.Condition{
			Type:               apiv1.ReadyConditionType,
			ObservedGeneration: comp.Generation,
			LastTransitionTime: metav1.Now(),
		}
	}

	// Readiness agg logic
	ok := true
	for _, gr := range grs.Items {
		if len(gr.OwnerReferences) == 0 || gr.OwnerReferences[0].UID != comp.UID {
			continue
		}
		grCond := meta.FindStatusCondition(gr.Status.Conditions, apiv1.ReadyConditionType)
		if grCond == nil || grCond.Status == metav1.ConditionFalse {
			ok = false
			break
		}
	}

	// Updates
	orig := cond.DeepCopy()
	if ok {
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Ready"
		cond.Message = "All resources are ready"
	} else {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "NotReady"
		cond.Message = "One or more resources is not ready"
	}

	// Writes
	if cond.Status == orig.Status {
		return ctrl.Result{}, nil // already in sync
	}
	meta.SetStatusCondition(&comp.Status.Conditions, *cond)
	err = c.client.Status().Update(ctx, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
	}

	c.logger.Info("updated composition readiness status")
	return ctrl.Result{}, nil
}
