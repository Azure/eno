package reconciliation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/go-logr/logr"
)

var insecureLogPatch = os.Getenv("INSECURE_LOG_PATCH") == "true"

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
	err := c.client.Get(ctx, types.NamespacedName{Name: req.Composition.Name, Namespace: req.Composition.Namespace}, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}

	if comp.Status.CurrentState == nil {
		return ctrl.Result{}, nil // nothing to do
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("synthesizerName", comp.Spec.Synthesizer.Name, "synthesizerGeneration", comp.Status.CurrentState.ObservedSynthesizerGeneration)
	ctx = logr.NewContext(ctx, logger)

	// Find the current and (optionally) previous desired states in the cache
	compRef := reconstitution.NewCompositionRef(comp)
	resource, exists := c.resourceClient.Get(ctx, compRef, &req.Resource)
	if !exists {
		// It's possible for the cache to be empty because a manifest for this resource no longer exists at the requested composition generation.
		// Dropping the work item is safe since filling the new version will generate a new queue message.
		logger.V(1).Info("dropping work item because the corresponding manifest generation no longer exists in the cache")
		return ctrl.Result{}, nil
	}

	var prev *reconstitution.Resource
	if comp.Status.PreviousState != nil {
		compRef.Generation = comp.Status.PreviousState.ObservedCompositionGeneration
		prev, _ = c.resourceClient.Get(ctx, compRef, &req.Resource)
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
	current, err := c.getCurrent(ctx, resource, apiVersion)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current state: %w", err)
	}

	// Nil current struct means the resource version hasn't changed since it was last observed
	// Skip without logging since this is a very hot path
	if current != nil {
		if err := c.reconcileResource(ctx, prev, resource, current); err != nil {
			return ctrl.Result{}, err
		}
	}

	c.resourceClient.PatchStatusAsync(ctx, &req.Manifest, func(rs *apiv1.ResourceState) (modified bool) {
		if resource.Manifest.Deleted && !rs.Deleted {
			rs.Deleted = true
			modified = true
		}
		if !rs.Reconciled {
			rs.Reconciled = true
			modified = true
		}
		return
	})

	if resource != nil && resource.Manifest.ReconcileInterval != nil {
		return ctrl.Result{RequeueAfter: wait.Jitter(resource.Manifest.ReconcileInterval.Duration, 0.1)}, nil
	}

	return ctrl.Result{}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) error {
	logger := logr.FromContextOrDiscard(ctx)

	if resource.Manifest.Deleted || resource.SliceDeleted {
		if current == nil || current.GetDeletionTimestamp() != nil {
			return nil // already deleted - nothing to do
		}

		err := c.upstreamClient.Delete(ctx, resource.Object)
		if err != nil {
			return client.IgnoreNotFound(fmt.Errorf("deleting resource: %w", err))
		}
		logger.V(0).Info("deleted resource")
		return nil
	}

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
	if insecureLogPatch {
		logger.V(1).Info("INSECURE logging patch", "patch", string(patch))
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
		prevManifest = []byte(prev.Manifest.Manifest)
	}

	currentJS, err := current.MarshalJSON()
	if err != nil {
		return nil, "", reconcile.TerminalError(fmt.Errorf("building json representation of desired state: %w", err))
	}

	model, err := c.discovery.Get(ctx, resource.Object.GroupVersionKind())
	if err != nil {
		return nil, "", fmt.Errorf("getting merge metadata: %w", err)
	}
	if model == nil {
		patch, err := jsonmergepatch.CreateThreeWayJSONMergePatch(prevManifest, []byte(resource.Manifest.Manifest), currentJS)
		if err != nil {
			return nil, "", reconcile.TerminalError(err)
		}
		return patch, types.MergePatchType, err
	}

	patchmeta := strategicpatch.NewPatchMetaFromOpenAPI(model)
	patch, err := strategicpatch.CreateThreeWayMergePatch(prevManifest, []byte(resource.Manifest.Manifest), currentJS, patchmeta, true)
	if err != nil {
		return nil, "", reconcile.TerminalError(err)
	}
	return patch, types.StrategicMergePatchType, err
}

func (c *Controller) getCurrent(ctx context.Context, resource *reconstitution.Resource, apiVersion string) (*unstructured.Unstructured, error) {
	if resource.HasBeenSeen() {
		meta := &metav1.PartialObjectMetadata{}
		meta.Name = resource.Ref.Name
		meta.Namespace = resource.Ref.Namespace
		meta.Kind = resource.Ref.Kind
		meta.APIVersion = apiVersion
		err := c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(meta), meta)
		if err != nil {
			return nil, err
		}
		if resource.MatchesLastSeen(meta.ResourceVersion) {
			return nil, nil // resource hasn't changed - no need to reconcile
		}
	}

	current := &unstructured.Unstructured{}
	current.SetName(resource.Ref.Name)
	current.SetNamespace(resource.Ref.Namespace)
	current.SetKind(resource.Ref.Kind)
	current.SetAPIVersion(apiVersion)
	err := c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(current), current)
	if rv := current.GetResourceVersion(); rv != "" {
		resource.ObserveVersion(rv)
	}

	return current, err
}

func mungePatch(patch []byte, rv string) ([]byte, error) {
	var patchMap map[string]interface{}
	err := json.Unmarshal(patch, &patchMap)
	if err != nil {
		return nil, reconcile.TerminalError(err)
	}
	u := unstructured.Unstructured{Object: patchMap}
	a, err := meta.Accessor(&u)
	if err != nil {
		return nil, reconcile.TerminalError(err)
	}
	a.SetResourceVersion(rv)
	a.SetCreationTimestamp(metav1.Time{})

	return json.Marshal(patchMap)
}
