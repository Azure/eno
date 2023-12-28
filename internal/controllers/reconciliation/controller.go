package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// TODO: Block ResourceSlice deletion until resources have been cleaned up
// TODO: Clean up unused resource slices older than a duration

// TODO: Handle 400s better

type Controller struct {
	client         client.Client
	resourceClient reconstitution.Client

	upstreamClient client.Client
	discovery      *discoveryCache
}

func New(mgr *reconstitution.Manager, downstream *rest.Config, discoveryRPS float32, rediscoverWhenNotFound bool) error {
	upstreamClient, err := client.New(downstream, client.Options{
		Scheme: runtime.NewScheme(), // empty scheme since we shouldn't rely on compile-time types
	})
	if err != nil {
		return err
	}

	disc, err := newDicoveryCache(downstream, discoveryRPS, rediscoverWhenNotFound)
	if err != nil {
		return err
	}

	return mgr.Add(&Controller{
		client:         mgr.Manager.GetClient(),
		resourceClient: mgr.GetClient(),
		upstreamClient: upstreamClient,
		discovery:      disc,
	})
}

func (c *Controller) Name() string { return "reconciliationController" }

func (c *Controller) Reconcile(ctx context.Context, req *reconstitution.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.Composition, comp)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting composition: %w", err)
	}

	if comp.Status.CurrentState == nil {
		return ctrl.Result{}, nil // nothing to do
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("synthesizerName", comp.Spec.Synthesizer.Name, "synthesizerGeneration", comp.Status.CurrentState.ObservedSynthesizerGeneration)
	ctx = logr.NewContext(ctx, logger)

	// Find the current and (optionally) previous desired states in the cache
	currentGen := comp.Status.CurrentState.ObservedCompositionGeneration
	resource, _ := c.resourceClient.Get(ctx, &req.ResourceRef, currentGen)

	var prev *reconstitution.Resource
	if comp.Status.PreviousState != nil {
		prev, _ = c.resourceClient.Get(ctx, &req.ResourceRef, comp.Status.PreviousState.ObservedCompositionGeneration)
	}

	// The current and previous resource can both be nil,
	// so we need to check both to find the apiVersion
	var apiVersion string
	if resource != nil {
		apiVersion = resource.Object.GetAPIVersion()
	} else if prev != nil {
		apiVersion = prev.Object.GetAPIVersion()
	} else {
		logger.Error(errors.New("no apiVersion provided"), "neither the current or previous resource have an apiVersion")
		return ctrl.Result{}, nil
	}

	// Fetch the current resource
	current := &unstructured.Unstructured{}
	current.SetName(req.Name)
	current.SetNamespace(req.Namespace)
	current.SetKind(req.Kind)
	current.SetAPIVersion(apiVersion)
	err = c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(current), current)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current state: %w", err)
	}

	// Do the reconciliation
	if err := c.reconcileResource(ctx, prev, resource, current); err != nil {
		return ctrl.Result{}, err
	}

	c.resourceClient.PatchStatusAsync(ctx, &req.Manifest, func(rs *apiv1.ResourceState) bool {
		if rs.Reconciled {
			return false // already in sync
		}
		rs.Reconciled = true
		return true
	})

	if resource != nil {
		return ctrl.Result{RequeueAfter: resource.ReconcileInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) error {
	logger := logr.FromContextOrDiscard(ctx)

	// TODO: Handle deletes here

	// Always create the resource when it doesn't exist
	if current.GetResourceVersion() == "" {
		err := c.upstreamClient.Create(ctx, resource.Object)
		if err != nil {
			return fmt.Errorf("creating resource: %w", err)
		}
		logger.V(0).Info("created resource")
		return nil
	}

	// Compute a merge patch
	prevRV := current.GetResourceVersion()
	patch, patchType, err := c.buildPatch(ctx, prev, resource, current)
	if err != nil {
		return fmt.Errorf("building patch: %w", err)
	}
	if string(patch) == "{}" {
		logger.V(1).Info("skipping empty patch")
		return nil
	}
	patch, err = mungePatch(patch, current.GetResourceVersion())
	if err != nil {
		return fmt.Errorf("adding resource version: %w", err)
	}
	err = c.upstreamClient.Patch(ctx, current, client.RawPatch(patchType, patch))
	if err != nil {
		return fmt.Errorf("applying patch: %w", err)
	}
	logger.V(0).Info("patched resource", "patchType", string(patchType), "resourceVersion", current.GetResourceVersion(), "previousResourceVersion", prevRV)

	return nil
}

func (c *Controller) buildPatch(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) ([]byte, types.PatchType, error) {
	var prevManifest []byte
	if prev != nil {
		prevManifest = []byte(prev.Manifest)
	}

	currentJS, err := current.MarshalJSON()
	if err != nil {
		return nil, "", fmt.Errorf("building json representation of desired state: %w", err)
	}

	model, err := c.discovery.Get(ctx, resource.Object.GroupVersionKind())
	if err != nil {
		return nil, "", fmt.Errorf("getting merge metadata: %w", err)
	}
	if model == nil {
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(prevManifest, []byte(resource.Manifest), currentJS)
		return patch, types.MergePatchType, err
	}

	patchmeta := strategicpatch.NewPatchMetaFromOpenAPI(model)
	patch, err := strategicpatch.CreateThreeWayMergePatch(prevManifest, []byte(resource.Manifest), currentJS, patchmeta, true)
	return patch, types.StrategicMergePatchType, err
}

func mungePatch(patch []byte, rv string) ([]byte, error) {
	var patchMap map[string]interface{}
	err := json.Unmarshal(patch, &patchMap)
	if err != nil {
		return nil, err
	}
	u := unstructured.Unstructured{Object: patchMap}
	a, err := meta.Accessor(&u)
	if err != nil {
		return nil, err
	}
	a.SetResourceVersion(rv)
	a.SetCreationTimestamp(metav1.Time{})

	return json.Marshal(patchMap)
}
