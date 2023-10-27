package statusagg

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/conf"
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
		Owns(&apiv1.GeneratedResourceSlice{}).
		Build(c)

	return err
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}
	if comp.Status.CompositionGeneration != comp.Generation {
		return ctrl.Result{}, nil
	}
	original := comp.DeepCopy()

	grs := &apiv1.GeneratedResourceSliceList{}
	err = c.client.List(ctx, grs, client.MatchingLabels{"composition": comp.Name})
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("listing generated resources: %w", err))
	}

	// Aggregation
	var ready, reconciled int64
	for _, gr := range grs.Items {
		if gr.Spec.DerivedGeneration != comp.Generation {
			continue
		}
		cond := meta.FindStatusCondition(gr.Status.Conditions, apiv1.ReadyConditionType)
		if cond != nil && cond.Status == metav1.ConditionTrue && cond.ObservedGeneration == comp.Generation {
			ready++
		}
		cond = meta.FindStatusCondition(gr.Status.Conditions, apiv1.ReconciledConditionType)
		if cond != nil && cond.Status == metav1.ConditionTrue && cond.ObservedGeneration == comp.Generation {
			reconciled++
		}
	}
	grCount := int64(len(grs.Items))

	// Condition writes
	readyCond := metav1.Condition{
		Type:               apiv1.ReadyConditionType,
		ObservedGeneration: comp.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if grCount == comp.Status.GeneratedResourceCount && ready == grCount {
		readyCond.Status = metav1.ConditionTrue
		readyCond.Reason = "Ready"
		readyCond.Message = "All resources are ready"
	} else {
		readyCond.Status = metav1.ConditionFalse
		readyCond.Reason = "NotReady"
		readyCond.Message = fmt.Sprintf("only %d out of %d are ready", ready, comp.Status.GeneratedResourceCount)
	}
	meta.SetStatusCondition(&comp.Status.Conditions, readyCond)

	recociledCond := metav1.Condition{
		Type:               apiv1.ReconciledConditionType,
		ObservedGeneration: comp.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if grCount == comp.Status.GeneratedResourceCount && reconciled == grCount {
		recociledCond.Status = metav1.ConditionTrue
		recociledCond.Reason = "Synced"
		recociledCond.Message = "All resources are in sync"
	} else {
		recociledCond.Status = metav1.ConditionFalse
		recociledCond.Reason = "OutOfSync"
		recociledCond.Message = fmt.Sprintf("only %d out of %d are in sync", reconciled, comp.Status.GeneratedResourceCount)
	}
	meta.SetStatusCondition(&comp.Status.Conditions, recociledCond)

	if equality.Semantic.DeepEqual(comp, original.Status) {
		return ctrl.Result{}, nil // no changes
	}
	err = c.client.Status().Update(ctx, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating composition status: %w", err)
	}

	c.logger.Info("updated aggregated composition status", "compositionName", comp.Name, "compositionGeneration", comp.Generation)
	return ctrl.Result{}, nil
}
