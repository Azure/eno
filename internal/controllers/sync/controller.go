package sync

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Azure/eno/internal/reconstitution"
)

// TODO: Logging

// TODO: Min reconcile interval? Or is this handled by the workqueue?

type Controller struct {
	upstreamClient client.Client
	resourceClient reconstitution.Client
}

func New(mgr *reconstitution.Manager) error {
	return mgr.Add(&Controller{
		upstreamClient: mgr.Manager.GetClient(), // TODO: Support separate client here, consider raw rest client to avoid json encode/decode hop
		resourceClient: mgr.GetClient(),
	})
}

func (c *Controller) Reconcile(ctx context.Context, req *reconstitution.GeneratedResourceMeta) (ctrl.Result, error) {
	resource, err := c.resourceClient.Get(ctx, req)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource: %w", err)
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
	if err := c.reconcileResource(ctx, resource, current); err != nil {
		return ctrl.Result{}, err
	}

	// Update status if it has drifted
	rv := current.GetResourceVersion()
	if !resource.Status.Synced || resource.Status.ObservedResourceVersion != rv {
		resource.Status.Synced = true
		resource.Status.ObservedResourceVersion = rv

		err = c.resourceClient.UpdateStatus(ctx, req, resource.Status)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: resource.Spec.ReconcileInterval}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, resource *reconstitution.GeneratedResource, current *unstructured.Unstructured) error {
	// Delete
	if false { // TODO: Logic
		if current.GetResourceVersion() == "" {
			return nil // already deleted
		}

		err := c.upstreamClient.Delete(ctx, resource.Spec.Object)
		if err != nil {
			return fmt.Errorf("deleting resource: %w", err)
		}
		return nil
	}

	// Create
	if current.GetResourceVersion() == "" {
		err := c.upstreamClient.Create(ctx, resource.Spec.Object)
		if err != nil {
			return fmt.Errorf("creating resource: %w", err)
		}
		return nil
	}

	// No need to reconcile if the actual and desired state haven't been written since last reconciliation
	if resource.Status.ObservedResourceVersion == current.GetResourceVersion() {
		return nil // this will not be reached when new generated resources are created because status.resourceVersion will be empty
	}

	// TODO: Support strategic patch where supported

	// Patch
	desiredJS, err := current.MarshalJSON()
	if err != nil {
		return fmt.Errorf("building json representation of desired state: %w", err)
	}
	patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch([]byte("TODO PREVIOUS MANIFEST"), []byte(resource.Spec.Manifest), desiredJS)
	if err != nil {
		return fmt.Errorf("building jsonpatch: %w", err)
	}
	if string(patch) == "{}" {
		return nil
	}
	err = c.upstreamClient.Patch(ctx, current, client.RawPatch(types.MergePatchType, patch))
	if err != nil {
		return fmt.Errorf("applying patch: %w", err)
	}

	return nil
}
