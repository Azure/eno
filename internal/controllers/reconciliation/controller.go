package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/jsonmergepatch"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/discovery"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/go-logr/logr"
)

// TODO: Set per-operation context timeouts

var insecureLogPatch = os.Getenv("INSECURE_LOG_PATCH") == "true"

type Controller struct {
	client                client.Client
	resourceClient        reconstitution.Client
	readinessPollInterval time.Duration

	upstreamClient client.Client
	discovery      *discovery.Cache
}

func New(mgr *reconstitution.Manager, downstream *rest.Config, discoveryRPS float32, rediscoverWhenNotFound bool, readinessPollInterval time.Duration) error {
	upstreamClient, err := client.New(downstream, client.Options{
		Scheme: runtime.NewScheme(), // empty scheme since we shouldn't rely on compile-time types
	})
	if err != nil {
		return err
	}

	disc, err := discovery.NewCache(downstream, discoveryRPS, rediscoverWhenNotFound)
	if err != nil {
		return err
	}

	return mgr.Add(&Controller{
		client:                mgr.Manager.GetClient(),
		resourceClient:        mgr.GetClient(),
		readinessPollInterval: readinessPollInterval,
		upstreamClient:        upstreamClient,
		discovery:             disc,
	})
}

func (c *Controller) Name() string { return "reconciliationController" }

func (c *Controller) Reconcile(ctx context.Context, req *reconstitution.Request) (ctrl.Result, error) {
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, types.NamespacedName{Name: req.Composition.Name, Namespace: req.Composition.Namespace}, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionGeneration", comp.Generation)

	if comp.Status.CurrentSynthesis == nil {
		return ctrl.Result{}, nil // nothing to do
	}
	logger = logger.WithValues("synthesizerName", comp.Spec.Synthesizer.Name, "synthesizerGeneration", comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration)
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
	if comp.Status.PreviousSynthesis != nil {
		compRef.Generation = comp.Status.PreviousSynthesis.ObservedCompositionGeneration
		prev, _ = c.resourceClient.Get(ctx, compRef, &req.Resource)
	}

	// Keep track of the last reconciliation time and report on it relative to the resource's reconcile interval
	// This is useful for identifying cases where the loop can't keep up
	if resource.Manifest.ReconcileInterval != nil {
		observation := resource.ObserveReconciliation()
		if observation > 0 {
			delta := observation - resource.Manifest.ReconcileInterval.Duration
			reconciliationScheduleDelta.Observe(delta.Seconds())
		}
	}

	// Fetch the current resource
	apiVersion, _ := resource.GVK.ToAPIVersionAndKind()
	current, hasChanged, err := c.getCurrent(ctx, resource, apiVersion)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, fmt.Errorf("getting current state: %w", err)
	}

	// Nil current struct means the resource version hasn't changed since it was last observed
	// Skip without logging since this is a very hot path
	var modified bool
	if hasChanged {
		modified, err = c.reconcileResource(ctx, comp, prev, resource, current)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Evaluate resource readiness
	// - Readiness checks are skipped when this version of the resource's desired state has already become ready
	// - Readiness checks are skipped when the resource hasn't changed since the last check
	// - Readiness defaults to true if no checks are given
	slice := &apiv1.ResourceSlice{}
	err = c.client.Get(ctx, req.Manifest.Slice, slice)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting resource slice: %w", err))
	}
	var ready *metav1.Time
	if status := req.Manifest.FindStatus(slice); status == nil || status.Ready == nil {
		readiness, ok := resource.ReadinessChecks.EvalOptionally(ctx, current)
		if ok {
			ready = &readiness.ReadyTime
		}
	}

	if modified {
		return ctrl.Result{Requeue: true}, nil
	}

	// Store the results
	deleted := current == nil || current.GetDeletionTimestamp() != nil
	c.resourceClient.PatchStatusAsync(ctx, &req.Manifest, patchResourceState(deleted, ready))
	if ready == nil {
		return ctrl.Result{RequeueAfter: wait.Jitter(c.readinessPollInterval, 0.1)}, nil
	}
	if resource != nil && !resource.Deleted() && resource.Manifest.ReconcileInterval != nil {
		return ctrl.Result{RequeueAfter: wait.Jitter(resource.Manifest.ReconcileInterval.Duration, 0.1)}, nil
	}
	return ctrl.Result{}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, comp *apiv1.Composition, prev, resource *reconstitution.Resource, current *unstructured.Unstructured) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	start := time.Now()
	defer func() {
		reconciliationLatency.Observe(time.Since(start).Seconds())
	}()

	if resource.Deleted() {
		if current == nil || current.GetDeletionTimestamp() != nil {
			return false, nil // already deleted - nothing to do
		}
		if comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan" {
			return false, nil
		}

		reconciliationActions.WithLabelValues("delete").Inc()
		obj, err := resource.Parse()
		if err != nil {
			return false, fmt.Errorf("invalid resource: %w", err)
		}
		err = c.upstreamClient.Delete(ctx, obj)
		if err != nil {
			return false, client.IgnoreNotFound(fmt.Errorf("deleting resource: %w", err))
		}
		logger.V(0).Info("deleted resource")
		return true, nil
	}

	// Always create the resource when it doesn't exist
	if current == nil {
		reconciliationActions.WithLabelValues("create").Inc()
		obj, err := resource.Parse()
		if err != nil {
			return false, fmt.Errorf("invalid resource: %w", err)
		}
		err = c.upstreamClient.Create(ctx, obj)
		if err != nil {
			return false, fmt.Errorf("creating resource: %w", err)
		}
		logger.V(0).Info("created resource")
		return true, nil
	}

	// Compute a merge patch
	prevRV := current.GetResourceVersion()
	patch, patchType, err := c.buildPatch(ctx, prev, resource, current)
	if err != nil {
		return false, fmt.Errorf("building patch: %w", err)
	}
	patch, err = mungePatch(patch, current.GetResourceVersion())
	if err != nil {
		return false, fmt.Errorf("adding resource version: %w", err)
	}
	if len(patch) == 0 {
		logger.V(1).Info("skipping empty patch")
		return false, nil
	}
	reconciliationActions.WithLabelValues("patch").Inc()
	if insecureLogPatch {
		logger.V(1).Info("INSECURE logging patch", "patch", string(patch))
	}
	err = c.upstreamClient.Patch(ctx, current, client.RawPatch(patchType, patch))
	if err != nil {
		return false, fmt.Errorf("applying patch: %w", err)
	}
	logger.V(0).Info("patched resource", "patchType", string(patchType), "resourceVersion", current.GetResourceVersion(), "previousResourceVersion", prevRV)

	return true, nil
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

	model, err := c.discovery.Get(ctx, resource.GVK)
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

func (c *Controller) getCurrent(ctx context.Context, resource *reconstitution.Resource, apiVersion string) (*unstructured.Unstructured, bool, error) {
	if resource.HasBeenSeen() && !resource.Deleted() {
		meta := &metav1.PartialObjectMetadata{}
		meta.Name = resource.Ref.Name
		meta.Namespace = resource.Ref.Namespace
		meta.Kind = resource.Ref.Kind
		meta.APIVersion = apiVersion
		err := c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(meta), meta)
		if err != nil {
			return nil, false, err
		}
		if resource.MatchesLastSeen(meta.ResourceVersion) {
			return nil, false, nil
		}
		resourceVersionChanges.Inc()
	}

	current := &unstructured.Unstructured{}
	current.SetName(resource.Ref.Name)
	current.SetNamespace(resource.Ref.Namespace)
	current.SetKind(resource.Ref.Kind)
	current.SetAPIVersion(apiVersion)
	err := c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(current), current)
	if err != nil {
		return nil, true, err
	}
	if rv := current.GetResourceVersion(); rv != "" {
		resource.ObserveVersion(rv)
	}
	return current, true, nil
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

	if len(patchMap) <= 1 {
		return nil, nil // resource version only == empty patch
	}

	return json.Marshal(patchMap)
}

func patchResourceState(deleted bool, ready *metav1.Time) reconstitution.StatusPatchFn {
	return func(rs *apiv1.ResourceState) *apiv1.ResourceState {
		if rs != nil && rs.Deleted == deleted && rs.Reconciled && ptr.Deref(rs.Ready, metav1.Time{}) == ptr.Deref(ready, metav1.Time{}) {
			return nil
		}
		return &apiv1.ResourceState{
			Deleted:    deleted,
			Ready:      ready,
			Reconciled: true,
		}
	}
}
