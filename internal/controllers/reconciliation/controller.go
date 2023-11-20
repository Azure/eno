package reconciliation

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/util/openapi"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/go-logr/logr"
)

type Controller struct {
	client         client.Client
	resourceClient reconstitution.Client

	upstreamClient client.Client
	openapi        openapi.Resources
}

func New(mgr *reconstitution.Manager, upstream *rest.Config) error {
	upstreamClient, err := client.New(upstream, client.Options{})
	if err != nil {
		return err
	}

	disc, err := discovery.NewDiscoveryClientForConfig(upstream)
	if err != nil {
		return err
	}
	disc.UseLegacyDiscovery = true // don't bother with aggregated APIs since they may be unavailable

	// TODO: Refresh sometimes?
	doc, err := disc.OpenAPISchema()
	if err != nil {
		return err
	}
	resources, err := openapi.NewOpenAPIData(doc)
	if err != nil {
		return err
	}

	return mgr.Add(&Controller{
		client:         mgr.Manager.GetClient(),
		resourceClient: mgr.GetClient(),
		upstreamClient: upstreamClient,
		openapi:        resources,
	})
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
		prev, _ = c.resourceClient.Get(ctx, &req.ResourceRef, comp.Status.PreviousState.ObservedCompositionGeneration)
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
	patch, err := c.buildPatch(ctx, prev, resource, current)
	if err != nil {
		return fmt.Errorf("building patch: %w", err)
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

func (c *Controller) buildPatch(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) ([]byte, error) {
	var prevManifest []byte
	if prev != nil {
		prevManifest = []byte(prev.Manifest)
	}

	desiredJS, err := current.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("building json representation of desired state: %w", err)
	}

	model := c.openapi.LookupResource(resource.Object.GroupVersionKind())
	if model == nil {
		// TODO: Remove?
		// Fall back to non-strategic merge
		logr.FromContextOrDiscard(ctx).Info("falling back to non-strategic merge patch because resource was not found in openapi spec")
		return jsonmergepatch.CreateThreeWayJSONMergePatch(prevManifest, []byte(resource.Manifest), desiredJS)
	}

	patchmeta := strategicpatch.NewPatchMetaFromOpenAPI(model)
	return strategicpatch.CreateThreeWayMergePatch([]byte(prevManifest), []byte(resource.Manifest), desiredJS, patchmeta, true)
}
