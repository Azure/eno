package sync

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/go-logr/logr"
)

type Controller struct {
	client, upstreamClient client.Client
	resourceClient         reconstitution.Client
	logger                 logr.Logger
}

func New(mgr *reconstitution.Manager, upstream client.Client) error {
	return mgr.Add(&Controller{
		client:         mgr.Manager.GetClient(),
		upstreamClient: upstream,
		resourceClient: mgr.GetClient(),
		logger:         mgr.GetLogger(),
	})
}

func (c *Controller) Reconcile(ctx context.Context, req *reconstitution.Request) (ctrl.Result, error) {
	gen := &apiv1.Generation{}
	err := c.client.Get(ctx, req.Generation, gen)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting generation: %w", err)
	}
	// TODO: Construct upstream from here
	logger := c.logger.WithValues("generationName", gen.Name, "generationNamespace", gen.Namespace, "generatorGeneration", gen.Generation, "resourceName", req.Name, "resourceNamespace", req.Namespace, "resourceKind", req.Kind)
	ctx = logr.NewContext(ctx, logger)

	if gen.Status.CurrentState == nil {
		logger.V(5).Info("generation is pending")
		return ctrl.Result{}, nil
	}
	currentGen := gen.Status.CurrentState.ObservedGeneration

	resource, err := c.resourceClient.Get(ctx, currentGen, &req.ResourceMeta)
	if errors.Is(err, reconstitution.ErrNotFound) {
		logger.V(3).Info("resource not found - dropping")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting current resource: %w", err)
	}

	var prev *reconstitution.Resource
	if gen.Status.PreviousState != nil {
		prev, err = c.resourceClient.Get(ctx, gen.Status.PreviousState.ObservedGeneration, &req.ResourceMeta)
		if errors.Is(err, reconstitution.ErrNotFound) {
			logger.V(5).Info("no previous resource manifest found")
			err = nil
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting previous resource: %w", err)
		}
	} else {
		logger.V(5).Info("no previous state given")
	}

	// Fetch the current resource
	current := &unstructured.Unstructured{}
	current.SetName(req.Name)
	current.SetNamespace(req.Namespace)
	current.SetKind(req.Kind)
	err = c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(current), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current state: %w", err)
	}

	// Do the reconciliation
	if err := c.reconcileResource(ctx, prev, resource, current); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(3).Info("sync'd resource")

	err = c.resourceClient.ObserveResource(ctx, req, currentGen, current.GetResourceVersion())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}

	return ctrl.Result{RequeueAfter: resource.Spec.ReconcileInterval}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) error {
	logger := logr.FromContextOrDiscard(ctx)

	// Delete
	if resource == nil {
		if current.GetResourceVersion() == "" {
			return nil // already deleted
		}

		logger.V(3).Info("deleting resource")
		err := c.upstreamClient.Delete(ctx, resource.Spec.Object)
		if err != nil {
			return fmt.Errorf("deleting resource: %w", err)
		}
		return nil
	}

	// Create
	if current.GetResourceVersion() == "" {
		logger.V(3).Info("creating resource")
		err := c.upstreamClient.Create(ctx, resource.Spec.Object)
		if err != nil {
			return fmt.Errorf("creating resource: %w", err)
		}
		return nil
	}

	// TODO: Support strategic patch where supported

	// Patch
	desiredJS, err := current.MarshalJSON()
	if err != nil {
		return fmt.Errorf("building json representation of desired state: %w", err)
	}
	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch([]byte(prev.Spec.Manifest), []byte(resource.Spec.Manifest), desiredJS)
	if err != nil {
		return fmt.Errorf("building jsonpatch: %w", err)
	}
	if string(patch) == "{}" {
		logger.V(5).Info("skipping empty patch")
		return nil
	}

	logger.V(3).Info("patching resource")
	err = c.upstreamClient.Patch(ctx, current, client.RawPatch(types.MergePatchType, patch))
	if err != nil {
		return fmt.Errorf("applying patch: %w", err)
	}

	return nil
}
