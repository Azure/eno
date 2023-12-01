package reconciliation

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/go-logr/logr"
)

// TODO: Minimal retries for validation error

type Controller struct {
	client         client.Client
	resourceClient reconstitution.Client

	upstreamClient client.Client
	discovery      *discoveryCache
}

func New(mgr *reconstitution.Manager, downstream *rest.Config, discoveryRPS float32, rediscoverWhenNotFound bool) (*Controller, error) { // TODO: REmove return
	upstreamClient, err := client.New(downstream, client.Options{
		Scheme: runtime.NewScheme(), // empty scheme since we shouldn't rely on compile-time types
	})
	if err != nil {
		return nil, err
	}

	disc, err := newDicoveryCache(downstream, discoveryRPS, rediscoverWhenNotFound)
	if err != nil {
		return nil, err
	}

	c := &Controller{
		client:         mgr.Manager.GetClient(),
		resourceClient: mgr.GetClient(),
		upstreamClient: upstreamClient,
		discovery:      disc,
	}
	return c, mgr.Add(c)
}

func (c *Controller) Name() string { return "reconciliationController" }

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
	currentGen := comp.Status.CurrentState.ObservedCompositionGeneration

	// Find the current and (optionally) previous desired states in the cache
	resource, found := c.resourceClient.Get(ctx, &req.ResourceRef, currentGen)
	if !found {
		logger.V(0).Info("resource not found - dropping")
		return ctrl.Result{}, nil
	}

	var prev *reconstitution.Resource
	if comp.Status.PreviousState != nil {
		var ok bool
		prev, ok = c.resourceClient.Get(ctx, &req.ResourceRef, comp.Status.PreviousState.ObservedCompositionGeneration)
		if !ok {
			logger.V(0).Info("previous resource not found - dropping") // TODO: error?
			return ctrl.Result{}, nil
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
	logger.V(1).Info("sync'd resource")

	c.resourceClient.PatchStatusAsync(ctx, &req.Manifest, func(rs *apiv1.ResourceState) bool {
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

	// TODO
	//
	// Delete
	// if prev == nil {
	// 	if current.GetResourceVersion() == "" || current.GetDeletionTimestamp() != nil {
	// 		return nil // already deleted
	// 	}

	// 	logger.V(0).Info("deleting resource")
	// 	err := c.upstreamClient.Delete(ctx, resource.Object)
	// 	if err != nil {
	// 		return fmt.Errorf("deleting resource: %w", err)
	// 	}
	// 	return nil
	// }

	// Create
	if current.GetResourceVersion() == "" {
		logger.V(0).Info("creating resource")
		err := c.upstreamClient.Create(ctx, resource.Object)
		if err != nil {
			return fmt.Errorf("creating resource: %w", err)
		}
		return nil
	}

	// Patch
	if prev == nil {
		return errors.New("TODO do a put instead?")
	}
	patch, patchType, err := c.buildPatch(ctx, prev, resource, current)
	if err != nil {
		return fmt.Errorf("building patch: %w", err)
	}
	if string(patch) == "{}" {
		logger.V(1).Info("skipping empty patch")
		return nil
	}

	logger.V(0).Info("patching resource", "patch", string(patch), "patchType", string(patchType), "currentResourceVersion", current.GetResourceVersion())
	err = c.upstreamClient.Patch(ctx, current, client.RawPatch(patchType, patch))
	if err != nil {
		return fmt.Errorf("applying patch: %w", err)
	}

	return nil
}

func (c *Controller) buildPatch(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) ([]byte, types.PatchType, error) {
	currentJS, err := current.MarshalJSON()
	if err != nil {
		return nil, "", fmt.Errorf("building json representation of desired state: %w", err)
	}

	prev.Object.SetResourceVersion(current.GetResourceVersion())
	prevJS, err := prev.Object.MarshalJSON()
	if err != nil {
		panic(err) // TODO
	}

	resource.Object.SetResourceVersion(current.GetResourceVersion())
	resourceJS, err := resource.Object.MarshalJSON()
	if err != nil {
		panic(err) // TODO
	}

	model, err := c.discovery.Get(ctx, current.GroupVersionKind()) // TODO: Change back?
	if err != nil {
		return nil, "", fmt.Errorf("getting merge metadata: %w", err)
	}
	if model == nil {
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch([]byte(prev.Manifest), []byte(resource.Manifest), currentJS)
		return patch, types.MergePatchType, err
	}

	println("TODO PATCH META", string(prevJS), string(resourceJS), string(currentJS))

	patchmeta := strategicpatch.NewPatchMetaFromOpenAPI(model)
	patch, err := strategicpatch.CreateThreeWayMergePatch([]byte(prevJS), []byte(resourceJS), currentJS, patchmeta, true)
	return patch, types.StrategicMergePatchType, err
}
