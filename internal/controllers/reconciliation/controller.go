package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/discovery"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/reconstitution"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
)

// TODO: Remember to remove requeue and just wait for status change

var insecureLogPatch = os.Getenv("INSECURE_LOG_PATCH") == "true"

type Options struct {
	Manager     ctrl.Manager
	Cache       *resource.Cache
	WriteBuffer *flowcontrol.ResourceSliceWriteBuffer
	Downstream  *rest.Config

	DiscoveryRPS float32

	Timeout               time.Duration
	ReadinessPollInterval time.Duration
}

type Controller struct {
	client                client.Client
	writeBuffer           *flowcontrol.ResourceSliceWriteBuffer
	cache                 *resource.Cache
	timeout               time.Duration
	readinessPollInterval time.Duration
	upstreamClient        client.Client
	discovery             *discovery.Cache
}

func New(opts Options) (*Controller, error) {
	upstreamClient, err := client.New(opts.Downstream, client.Options{
		Scheme: runtime.NewScheme(), // empty scheme since we shouldn't rely on compile-time types
	})
	if err != nil {
		return nil, err
	}

	disc, err := discovery.NewCache(opts.Downstream, opts.DiscoveryRPS)
	if err != nil {
		return nil, err
	}

	return &Controller{
		client:                opts.Manager.GetClient(),
		writeBuffer:           opts.WriteBuffer,
		cache:                 opts.Cache,
		timeout:               opts.Timeout,
		readinessPollInterval: opts.ReadinessPollInterval,
		upstreamClient:        upstreamClient,
		discovery:             disc,
	}, nil
}

func (c *Controller) Reconcile(ctx context.Context, req *resource.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, types.NamespacedName{Name: req.Composition.Name, Namespace: req.Composition.Namespace}, comp)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("getting composition: %w", err))
	}
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionGeneration", comp.Generation)

	if comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Failed() {
		return ctrl.Result{}, nil // nothing to do
	}
	logger = logger.WithValues("synthesizerName", comp.Spec.Synthesizer.Name,
		"synthesizerGeneration", comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration,
		"synthesisID", comp.Status.GetCurrentSynthesisUUID())
	ctx = logr.NewContext(ctx, logger)

	// Find the current and (optionally) previous desired states in the cache
	// TODO: Also pass comp name instead of just UUID?
	synRef := reconstitution.NewSynthesisRef(comp)
	res, exists := c.cache.Get(synRef.UUID, &req.Resource)
	if !exists {
		logger.V(1).Info("dropping work item because the corresponding synthesis no longer exists in the cache")
		return ctrl.Result{}, nil
	}

	var prev *resource.Resource
	if comp.Status.PreviousSynthesis != nil {
		prev, _ = c.cache.Get(comp.Status.PreviousSynthesis.UUID, &req.Resource)
	}
	logger = logger.WithValues("resourceKind", res.Ref.Kind, "resourceName", res.Ref.Name, "resourceNamespace", res.Ref.Namespace)
	ctx = logr.NewContext(ctx, logger)

	// Keep track of the last reconciliation time and report on it relative to the resource's reconcile interval
	// This is useful for identifying cases where the loop can't keep up
	if res.ReconcileInterval != nil {
		observation := res.ObserveReconciliation()
		if observation > 0 {
			delta := observation - res.ReconcileInterval.Duration
			reconciliationScheduleDelta.Observe(delta.Seconds())
		}
	}

	// Fetch the current resource
	current, err := c.getCurrent(ctx, res)
	if client.IgnoreNotFound(err) != nil && !isErrMissingNS(err) {
		return ctrl.Result{}, fmt.Errorf("getting current state: %w", err)
	}

	// Evaluate resource readiness
	// - Readiness checks are skipped when this version of the resource's desired state has already become ready
	// - Readiness checks are skipped when the resource hasn't changed since the last check
	// - Readiness defaults to true if no checks are given
	// TODO: Shouldn't need to get slice here
	slice := &apiv1.ResourceSlice{}
	err = c.client.Get(ctx, res.ManifestRef.Slice, slice)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting resource slice: %w", err)
	}
	var ready *metav1.Time
	status := res.FindStatus(slice)
	if status == nil || status.Ready == nil {
		readiness, ok := res.ReadinessChecks.EvalOptionally(ctx, current)
		if ok {
			ready = &readiness.ReadyTime
		}
	} else {
		ready = status.Ready
	}

	// Bail out if the resource isn't ready to be reconciled
	if (status == nil || !status.Reconciled) && !res.Deleted(comp) && !c.cache.Visible(synRef.UUID, &res.Ref) {
		return ctrl.Result{}, nil
	}

	modified, err := c.reconcileResource(ctx, comp, prev, res, current)
	if err != nil {
		return ctrl.Result{}, err
	}
	// If we modified the resource, we should also re-evaluate readiness
	// without waiting for the interval.
	if modified {
		return ctrl.Result{Requeue: true}, nil
	}

	deleted := current == nil ||
		current.GetDeletionTimestamp() != nil ||
		(res.Deleted(comp) && comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan") // orphaning should be reflected on the status.
	c.writeBuffer.PatchStatusAsync(ctx, &res.ManifestRef, patchResourceState(deleted, ready))
	if ready == nil {
		return ctrl.Result{RequeueAfter: wait.Jitter(c.readinessPollInterval, 0.1)}, nil
	}
	if res != nil && !res.Deleted(comp) && res.ReconcileInterval != nil {
		return ctrl.Result{RequeueAfter: wait.Jitter(res.ReconcileInterval.Duration, 0.1)}, nil
	}
	return ctrl.Result{}, nil
}

func (c *Controller) reconcileResource(ctx context.Context, comp *apiv1.Composition, prev, resource *resource.Resource, current *unstructured.Unstructured) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	start := time.Now()
	defer func() {
		reconciliationLatency.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if resource.Deleted(comp) {
		if current == nil || current.GetDeletionTimestamp() != nil {
			return false, nil // already deleted - nothing to do
		}
		if comp.Annotations["eno.azure.io/deletion-strategy"] == "orphan" {
			return false, nil
		}

		reconciliationActions.WithLabelValues("delete").Inc()
		err := c.upstreamClient.Delete(ctx, current)
		if err != nil {
			return true, client.IgnoreNotFound(fmt.Errorf("deleting resource: %w", err))
		}
		logger.V(0).Info("deleted resource")
		return true, nil
	}

	if resource.Patch != nil && current == nil {
		logger.V(1).Info("resource doesn't exist - skipping patch")
		return false, nil
	}

	// Create the resource when it doesn't exist
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

	if resource.DisableUpdates {
		return false, nil
	}

	// Apply Eno patches
	if resource.Patch != nil {
		if !resource.NeedsToBePatched(current) {
			return false, nil
		}
		patch, err := json.Marshal(&resource.Patch)
		if err != nil {
			return false, fmt.Errorf("encoding json patch: %w", err)
		}

		err = c.upstreamClient.Patch(ctx, current, client.RawPatch(types.JSONPatchType, patch))
		if err != nil {
			return false, fmt.Errorf("applying patch: %w", err)
		}

		reconciliationActions.WithLabelValues("patch").Inc()
		logger.V(0).Info("patched resource", "resourceVersion", current.GetResourceVersion())
		return true, nil
	}

	// Compute a merge patch
	updated, typed, err := resource.Merge(ctx, prev, current, c.discovery)
	if err != nil {
		return false, fmt.Errorf("performing three-way merge: %w", err)
	}
	if updated == nil {
		logger.V(1).Info("skipping empty update")
		return false, nil
	}
	if insecureLogPatch {
		js, _ := updated.MarshalJSON()
		logger.V(1).Info("INSECURE logging patch", "update", string(js))
	}

	err = c.upstreamClient.Update(ctx, updated)
	if err != nil {
		return false, fmt.Errorf("applying update: %w", err)
	}

	reconciliationActions.WithLabelValues("patch").Inc()
	logger.V(0).Info("updated resource", "resourceVersion", updated.GetResourceVersion(), "previousResourceVersion", current.GetResourceVersion(), "typedMerge", typed)
	return true, nil
}

func (c *Controller) getCurrent(ctx context.Context, resource *resource.Resource) (*unstructured.Unstructured, error) {
	current := &unstructured.Unstructured{}
	current.SetName(resource.Ref.Name)
	current.SetNamespace(resource.Ref.Namespace)
	current.SetKind(resource.GVK.Kind)
	current.SetAPIVersion(resource.GVK.GroupVersion().String())
	err := c.upstreamClient.Get(ctx, client.ObjectKeyFromObject(current), current)
	if err != nil {
		return nil, err
	}
	return current, nil
}

func patchResourceState(deleted bool, ready *metav1.Time) flowcontrol.StatusPatchFn {
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

// isErrMissingNS returns true when given the client-go error returned by mutating requests that do not include a namespace.
// Sadly, this error isn't exposed anywhere - it's just a plain string, so we have to do string matching here.
//
// https://github.com/kubernetes/kubernetes/blob/9edabd617945cd23111fd46cfc9a09fe37ed194a/staging/src/k8s.io/client-go/rest/request.go#L1048
func isErrMissingNS(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "an empty namespace may not be set")
}
