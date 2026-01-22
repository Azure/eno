package watch

import (
	"context"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type pruningController struct {
	client client.Client
}

func (c *pruningController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "synthesizerName", comp.Spec.Synthesizer.Name,
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)

	synth := &apiv1.Synthesizer{}
	synth.Name = comp.Spec.Synthesizer.Name
	err = c.client.Get(ctx, client.ObjectKeyFromObject(synth), synth)
	if client.IgnoreNotFound(err) != nil {
		logger.Error(err, "failed to get synthesizer")
		return ctrl.Result{}, err
	}

	for i, ir := range comp.Status.InputRevisions {
		if hasBindingKey(comp, synth, ir.Key) {
			continue
		}
		logger.Info("pruning input revision - no longer has binding", "key", ir.Key, "revision", ir.Revision, "index", i)
		comp.Status.InputRevisions = append(comp.Status.InputRevisions[:i], comp.Status.InputRevisions[i+1:]...)
		err = c.client.Status().Update(ctx, comp)
		if err != nil {
			logger.Error(err, "failed to update composition status")
			return ctrl.Result{}, err
		}

		logger.Info("pruned old input revision from composition status", "compositionName", comp.Name, "compositionNamespace", comp.Namespace, "ref", ir.Key)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func hasBindingKey(comp *apiv1.Composition, synth *apiv1.Synthesizer, key string) bool {
	for _, b := range comp.Spec.Bindings {
		if b.Key == key {
			return true
		}
	}
	for _, ref := range synth.Spec.Refs {
		if ref.Key == key && ref.Resource.Name != "" {
			return true // implicit binding
		}
	}
	return false
}
