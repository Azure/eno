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
}

func New(mgr *reconstitution.Manager, upstream client.Client) error {
	return mgr.Add(&Controller{
		client:         mgr.Manager.GetClient(),
		upstreamClient: upstream,
		resourceClient: mgr.GetClient(),
	})
}

func (c *Controller) Name() string { return "syncController" }

func (c *Controller) Reconcile(ctx context.Context, req *reconstitution.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.Composition, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
	}

	if comp.Status.CurrentState == nil {
		logger.V(1).Info("composition has not yet been synthesized")
		return ctrl.Result{}, nil
	}
	currentGen := comp.Status.CurrentState.ObservedGeneration

	resource, err := c.resourceClient.Get(ctx, currentGen, &req.ResourceRef)
	if errors.Is(err, reconstitution.ErrNotFound) {
		logger.V(0).Info("resource not found - dropping")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting current resource: %w", err)
	}

	var prev *reconstitution.Resource
	if comp.Status.PreviousState != nil {
		prev, err = c.resourceClient.Get(ctx, comp.Status.PreviousState.ObservedGeneration, &req.ResourceRef)
		if errors.Is(err, reconstitution.ErrNotFound) {
			logger.V(1).Info("no previous resource manifest found")
			err = nil
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("getting previous resource: %w", err)
		}
	} else {
		logger.V(1).Info("no previous state given")
	}

	// Fetch the current resource
	current := &unstructured.Unstructured{}
	current.SetName(req.Name)
	current.SetNamespace(req.Namespace)
	current.SetKind(req.Kind)
	current.SetAPIVersion(resource.Object.GetAPIVersion())
	err = c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(current), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current state: %w", err)
	}

	// Do the reconciliation
	if err := c.reconcileResource(ctx, prev, resource, current); err != nil {
		return ctrl.Result{}, err
	}
	logger.V(0).Info("sync'd resource")

	c.resourceClient.PatchStatusAsync(ctx, req, func(rs *apiv1.ResourceStatus) bool {
		if rs.Reconciled {
			return false // already in sync
		}
		rs.Reconciled = true
		return true
	})

	return ctrl.Result{RequeueAfter: resource.ReconcileInterval}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) error {
	logger := logr.FromContextOrDiscard(ctx)

	// Delete
	if resource == nil {
		if current.GetResourceVersion() == "" {
			return nil // already deleted
		}

		logger.V(0).Info("deleting resource")
		err := c.upstreamClient.Delete(ctx, resource.Object)
		if err != nil {
			return fmt.Errorf("deleting resource: %w", err)
		}
		return nil
	}

	// Create
	if current.GetResourceVersion() == "" {
		logger.V(0).Info("creating resource")
		err := c.upstreamClient.Create(ctx, resource.Object)
		if err != nil {
			return fmt.Errorf("creating resource: %w", err)
		}
		return nil
	}

	// TODO: Support strategic patch where supported

	var prevManifest []byte
	if prev != nil {
		prevManifest = []byte(prev.Manifest)
	}

	// Patch
	desiredJS, err := current.MarshalJSON()
	if err != nil {
		return fmt.Errorf("building json representation of desired state: %w", err)
	}
	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(prevManifest, []byte(resource.Manifest), desiredJS)
	if err != nil {
		return fmt.Errorf("building jsonpatch: %w", err)
	}
	if string(patch) == "{}" {
		logger.V(1).Info("skipping empty patch")
		return nil
	}

	logger.V(0).Info("patching resource")
	err = c.upstreamClient.Patch(ctx, current, client.RawPatch(types.MergePatchType, patch))
	if err != nil {
		return fmt.Errorf("applying patch: %w", err)
	}

	return nil
}