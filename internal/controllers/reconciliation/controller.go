package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/flowcontrol"
	"github.com/Azure/eno/internal/manager"
	"github.com/Azure/eno/internal/resource"
	"github.com/go-logr/logr"
)

type Options struct {
	Manager     ctrl.Manager
	WriteBuffer *flowcontrol.ResourceSliceWriteBuffer
	Downstream  *rest.Config

	DisableServerSideApply bool

	Timeout               time.Duration
	ReadinessPollInterval time.Duration
	MinReconcileInterval  time.Duration
}

type Controller struct {
	client                client.Client
	writeBuffer           *flowcontrol.ResourceSliceWriteBuffer
	resourceClient        *resource.Cache
	timeout               time.Duration
	readinessPollInterval time.Duration
	upstreamClient        client.Client
	minReconcileInterval  time.Duration
	disableSSA            bool
}

func New(mgr ctrl.Manager, opts Options) error {
	upstreamClient, err := client.New(opts.Downstream, client.Options{
		Scheme: runtime.NewScheme(), // empty scheme since we shouldn't rely on compile-time types
	})
	if err != nil {
		return err
	}

	src, cache, err := newReconstitutionSource(mgr)
	if err != nil {
		return err
	}

	c := &Controller{
		client:                opts.Manager.GetClient(),
		writeBuffer:           opts.WriteBuffer,
		resourceClient:        cache,
		timeout:               opts.Timeout,
		readinessPollInterval: opts.ReadinessPollInterval,
		upstreamClient:        upstreamClient,
		minReconcileInterval:  opts.MinReconcileInterval,
		disableSSA:            opts.DisableServerSideApply,
	}

	return builder.TypedControllerManagedBy[resource.Request](mgr).
		Named("reconciliationController").
		WithLogConstructor(manager.NewTypedLogConstructor[*resource.Request](mgr, "reconciliationController")).
		WithOptions(controller.TypedOptions[resource.Request]{
			// Since this controller uses requeues as feedback instead of watches, the default
			// rate limiter's global 10 RPS token bucket quickly becomes a bottleneck.
			//
			// This rate limiter uses the same per-item rate limiter as the default, but without
			// the additional shared/global/non-item-scoped limiter.
			RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[resource.Request](5*time.Millisecond, 1000*time.Second),
		}).
		WatchesRawSource(src).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req resource.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, types.NamespacedName{Name: req.Composition.Name, Namespace: req.Composition.Namespace}, comp)
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	synthesisUUID := comp.Status.GetCurrentSynthesisUUID()
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisUUID", synthesisUUID)

	if comp.Status.CurrentSynthesis == nil {
		return ctrl.Result{}, nil // nothing to do
	}
	logger = logger.WithValues("synthesizerName", comp.Spec.Synthesizer.Name, "synthesizerGeneration", comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration, "synthesisUUID", comp.Status.GetCurrentSynthesisUUID())
	ctx = logr.NewContext(ctx, logger)

	// Find the current and (optionally) previous desired states in the cache
	var prev *resource.Resource
	resource, visible, exists := c.resourceClient.Get(ctx, synthesisUUID, req.Resource)
	if !exists || !visible {
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("resourceKind", resource.Ref.Kind, "resourceName", resource.Ref.Name, "resourceNamespace", resource.Ref.Namespace)
	ctx = logr.NewContext(ctx, logger)

	if syn := comp.Status.PreviousSynthesis; syn != nil {
		prev, _, _ = c.resourceClient.Get(ctx, syn.UUID, req.Resource)
	}

	// Fetch the current resource
	current, err := c.getCurrent(ctx, resource)
	if client.IgnoreNotFound(err) != nil && !isErrMissingNS(err) && !isErrNoKindMatch(err) {
		logger.Error(err, "failed to get current state")
		return ctrl.Result{}, err
	}

	// Evaluate resource readiness
	// - Readiness checks are skipped when this version of the resource's desired state has already become ready
	// - Readiness checks are skipped when the resource hasn't changed since the last check
	// - Readiness defaults to true if no checks are given
	var ready *metav1.Time
	status := resource.State()
	if status == nil || status.Ready == nil {
		readiness, ok := resource.ReadinessChecks.EvalOptionally(ctx, current)
		if ok {
			ready = &readiness.ReadyTime
		}
	} else {
		ready = status.Ready
	}

	modified, err := c.reconcileResource(ctx, comp, prev, resource, current)
	if err != nil {
		logger.Error(err, "failed to reconcile resource")
		return ctrl.Result{}, err
	}
	if modified {
		return ctrl.Result{Requeue: true}, nil
	}

	deleted := current == nil ||
		current.GetDeletionTimestamp() != nil ||
		(resource.Deleted(comp) && comp.ShouldOrphanResources()) // orphaning should be reflected on the status.
	c.writeBuffer.PatchStatusAsync(ctx, &resource.ManifestRef, patchResourceState(deleted, ready))

	return c.requeue(logger, comp, resource, ready)
}

func (c *Controller) reconcileResource(ctx context.Context, comp *apiv1.Composition, prev, res *resource.Resource, current *unstructured.Unstructured) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)
	start := time.Now()
	defer func() {
		reconciliationLatency.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if res.Deleted(comp) {
		if current == nil || current.GetDeletionTimestamp() != nil {
			return false, nil // already deleted - nothing to do
		}

		reconciliationActions.WithLabelValues("delete").Inc()
		err := c.upstreamClient.Delete(ctx, current)
		if err != nil {
			return true, client.IgnoreNotFound(fmt.Errorf("deleting resource: %w", err))
		}
		logger.V(0).Info("deleted resource")
		return true, nil
	}

	if res.Patch != nil && current == nil {
		logger.V(1).Info("resource doesn't exist - skipping patch")
		return false, nil
	}

	// Create the resource when it doesn't exist, should exist, and wouldn't be created later by server-side apply
	if current == nil && (res.DisableUpdates || res.Replace || c.disableSSA) {
		reconciliationActions.WithLabelValues("create").Inc()
		err := c.upstreamClient.Create(ctx, res.Unstructured())
		if err != nil {
			return false, fmt.Errorf("creating resource: %w", err)
		}
		logger.V(0).Info("created resource")
		return true, nil
	}

	if res.DisableUpdates {
		return false, nil
	}

	// Apply Eno patches
	if res.Patch != nil {
		if !res.NeedsToBePatched(current) {
			return false, nil
		}
		patch, err := json.Marshal(&res.Patch)
		if err != nil {
			return false, fmt.Errorf("encoding json patch: %w", err)
		}

		reconciliationActions.WithLabelValues("patch").Inc()
		err = c.upstreamClient.Patch(ctx, current, client.RawPatch(types.JSONPatchType, patch))
		if err != nil {
			return false, fmt.Errorf("applying patch: %w", err)
		}

		logger.V(0).Info("patched resource", "resourceVersion", current.GetResourceVersion())
		return true, nil
	}

	// Dry-run the update to see if it's needed
	dryRun, err := c.update(ctx, res, current, true)
	if err != nil {
		return false, fmt.Errorf("dry-run applying update: %w", err)
	}
	if resource.Compare(dryRun, current) {
		return false, nil // in sync
	}

	// When using server side apply, make sure we haven't lost any managedFields metadata.
	// Eno should always remove fields that are no longer set by the synthesizer, even if another client messed with managedFields.
	if current != nil && !c.disableSSA && !res.Replace {
		var dryRunPrev *unstructured.Unstructured
		if prev != nil {
			dryRunPrev = prev.Unstructured()
			err = c.upstreamClient.Patch(ctx, dryRunPrev, client.Apply, client.ForceOwnership, client.FieldOwner("eno"), client.DryRunAll)
			if err != nil {
				return false, fmt.Errorf("getting managed fields values for previous version: %w", err)
			}
		}

		outOfSyncWithPrevious := dryRunPrev != nil && !resource.CompareEnoManagedFields(dryRunPrev.GetManagedFields(), current.GetManagedFields())
		outOfSyncWithCurrent := !resource.CompareEnoManagedFields(dryRun.GetManagedFields(), current.GetManagedFields())

		if (outOfSyncWithPrevious && outOfSyncWithCurrent) || (outOfSyncWithCurrent && dryRunPrev == nil) {
			if dryRunPrev == nil {
				current.SetManagedFields(
					resource.MergeEnoManagedFields(
						current.GetManagedFields(), dryRun.GetManagedFields()))
			} else {
				current.SetManagedFields(
					resource.MergeEnoManagedFields(
						current.GetManagedFields(), dryRunPrev.GetManagedFields()))
			}

			err := c.upstreamClient.Update(ctx, current, client.FieldOwner("eno"))
			if err != nil {
				return false, fmt.Errorf("updating managed fields metadata: %w", err)
			}
			logger.V(0).Info("corrected drift in managed fields metadata")
			return true, nil
		}
	}

	// Do the actual non-dryrun update
	reconciliationActions.WithLabelValues("apply").Inc()
	updated, err := c.update(ctx, res, current, false)
	if err != nil {
		return false, fmt.Errorf("applying update: %w", err)
	}
	if current != nil && updated.GetResourceVersion() == current.GetResourceVersion() {
		logger.V(0).Info("resource didn't change after update")
		return false, nil
	}
	if current != nil {
		logger = logger.WithValues("oldResourceVersion", current.GetResourceVersion())
	}
	logger.V(0).Info("applied resource", "resourceVersion", updated.GetResourceVersion(), "dryRunResourceVersion", dryRun.GetResourceVersion())
	return true, nil
}

func (c *Controller) update(ctx context.Context, resource *resource.Resource, current *unstructured.Unstructured, dryrun bool) (updated *unstructured.Unstructured, err error) {
	updated = resource.Unstructured()

	if current != nil {
		updated.SetResourceVersion(current.GetResourceVersion())
	}

	if resource.Replace {
		opts := []client.UpdateOption{}
		if dryrun {
			opts = append(opts, client.DryRunAll)
		}
		err = c.upstreamClient.Update(ctx, updated, opts...)
		return
	}

	opts := []client.PatchOption{}
	if dryrun {
		opts = append(opts, client.DryRunAll)
	}

	var patch client.Patch
	if c.disableSSA {
		// When SSA is disabled, use StrategicMergeFrom which preserves fields
		// not explicitly managed by the patch
		if current != nil {
			patch = client.StrategicMergeFrom(current)
		} else {
			// If there's no current object (shouldn't happen due to checks above),
			// fall back to MergeFrom as a safety measure
			patch = client.MergeFrom(current)
		}
	} else {
		patch = client.Apply
		opts = append(opts, client.ForceOwnership, client.FieldOwner("eno"))
	}

	err = c.upstreamClient.Patch(ctx, updated, patch, opts...)
	return
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

func (c *Controller) requeue(logger logr.Logger, comp *apiv1.Composition, resource *resource.Resource, ready *metav1.Time) (ctrl.Result, error) {
	if ready == nil {
		return ctrl.Result{RequeueAfter: wait.Jitter(c.readinessPollInterval, 0.1)}, nil
	}

	if resource == nil || resource.Deleted(comp) || resource.ReconcileInterval == nil {
		return ctrl.Result{}, nil
	}

	interval := resource.ReconcileInterval.Duration
	if interval < c.minReconcileInterval {
		logger.V(1).Info("reconcile interval is too small - using default", "latency", interval, "default", c.minReconcileInterval)
		interval = c.minReconcileInterval
	}
	return ctrl.Result{RequeueAfter: wait.Jitter(interval, 0.1)}, nil
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

func isErrNoKindMatch(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no matches for kind")
}
