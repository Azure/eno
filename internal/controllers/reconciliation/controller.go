package reconciliation

import (
	"context"
	goerrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/api/errors"
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
	Manager        ctrl.Manager
	WriteBuffer    *flowcontrol.ResourceSliceWriteBuffer
	Downstream     *rest.Config
	ResourceFilter cel.Program

	DisableServerSideApply bool
	FailOpen               bool
	MigratingFieldManagers []string

	Timeout               time.Duration
	ReadinessPollInterval time.Duration
	MinReconcileInterval  time.Duration
}

type Controller struct {
	client                 client.Client
	writeBuffer            *flowcontrol.ResourceSliceWriteBuffer
	resourceClient         *resource.Cache
	resourceFilter         cel.Program
	timeout                time.Duration
	readinessPollInterval  time.Duration
	upstreamClient         client.Client
	minReconcileInterval   time.Duration
	disableSSA             bool
	failOpen               bool
	migratingFieldManagers []string
}

func New(mgr ctrl.Manager, opts Options) error {
	upstreamClient, err := client.New(opts.Downstream, client.Options{
		Scheme: runtime.NewScheme(), // empty scheme since we shouldn't rely on compile-time types
	})
	if err != nil {
		return err
	}

	src, cache, err := newReconstitutionSource(mgr, opts.ResourceFilter)
	if err != nil {
		return err
	}

	c := &Controller{
		client:                 opts.Manager.GetClient(),
		writeBuffer:            opts.WriteBuffer,
		resourceClient:         cache,
		resourceFilter:         opts.ResourceFilter,
		timeout:                opts.Timeout,
		readinessPollInterval:  opts.ReadinessPollInterval,
		upstreamClient:         upstreamClient,
		minReconcileInterval:   opts.MinReconcileInterval,
		disableSSA:             opts.DisableServerSideApply,
		failOpen:               opts.FailOpen,
		migratingFieldManagers: opts.MigratingFieldManagers,
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
	logger.Info("reconciling resource", "compositionName", req.Composition.Name, "compositionNamespace", req.Composition.Namespace, "resourceRef", req.Resource.String())

	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, types.NamespacedName{Name: req.Composition.Name, Namespace: req.Composition.Namespace}, comp)
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}
	synthesisUUID := comp.Status.GetCurrentSynthesisUUID()
	logger = logger.WithValues("compositionName", comp.Name, "compositionNamespace", comp.Namespace, "compositionGeneration", comp.Generation, "synthesisUUID", synthesisUUID,
		"operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)

	if comp.Status.CurrentSynthesis == nil {
		return ctrl.Result{}, nil // nothing to do
	}
	logger = logger.WithValues("synthesizerName", comp.Spec.Synthesizer.Name, "synthesizerGeneration", comp.Status.CurrentSynthesis.ObservedSynthesizerGeneration, "synthesisUUID", comp.Status.GetCurrentSynthesisUUID())
	ctx = logr.NewContext(ctx, logger)

	// Find the current and (optionally) previous desired states in the cache
	logger.Info("retrieving resource from cache", "synthesisUUID", synthesisUUID)
	var prev *resource.Resource
	resource, visible, exists := c.resourceClient.Get(ctx, synthesisUUID, req.Resource)
	if !exists {
		logger.Info("resource not found in cache, skipping", "synthesisUUID", synthesisUUID)
		return ctrl.Result{}, nil
	}
	if !visible {
		logger.Info("resource currently not visible, skipping", "synthesisUUID", synthesisUUID)
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("resourceKind", resource.Ref.Kind, "resourceName", resource.Ref.Name, "resourceNamespace", resource.Ref.Namespace)
	ctx = logr.NewContext(ctx, logger)

	if syn := comp.Status.PreviousSynthesis; syn != nil {
		prev, _, _ = c.resourceClient.Get(ctx, syn.UUID, req.Resource)
		logger.Info("retrieved previous synthesis from cache", "previousSynthesisUUID", syn.UUID, "hasPrevious", prev != nil)
	}

	logger.Info("reconcileResource")
	snap, current, ready, modified, err := c.reconcileResource(ctx, comp, prev, resource)
	failingOpen := c.shouldFailOpen(resource)
	if failingOpen {
		logger.Info("FailOpen - suppressing errors")
		err = nil
		modified = false
	}
	if err != nil {
		logger.Error(err, "resource reconciliation failed")
		c.writeBuffer.PatchStatusAsync(ctx, &resource.ManifestRef, patchResourceError(ctx, err))
		return ctrl.Result{}, err
	}
	if modified {
		logger.Info("resource was modified, requeueing")
		return ctrl.Result{Requeue: true}, nil
	}

	deleted := current == nil ||
		(current.GetDeletionTimestamp() != nil && !snap.ForegroundDeletion) ||
		(snap.Deleted() && (snap.Orphan || snap.Disable || failingOpen)) // orphaning should be reflected on the status.

	c.writeBuffer.PatchStatusAsync(ctx, &resource.ManifestRef, patchResourceState(deleted, ready))
	return c.requeue(logger, snap, ready)
}

func (c *Controller) shouldFailOpen(resource *resource.Resource) bool {
	return (resource.FailOpen == nil && c.failOpen) || (resource.FailOpen != nil && *resource.FailOpen)
}

func (c *Controller) reconcileResource(ctx context.Context, comp *apiv1.Composition, prev *resource.Resource, resource *resource.Resource) (snap *resource.Snapshot, current *unstructured.Unstructured, ready *metav1.Time, modified bool, err error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Fetch the current resource
	current, err = c.getCurrent(ctx, resource)
	logger.Info("fetching current state of resource")
	if client.IgnoreNotFound(err) != nil && !isErrMissingNS(err) && !isErrNoKindMatch(err) {
		logger.Error(err, "failed to get current state")
		return nil, nil, nil, false, err
	}

	// Evaluate resource readiness
	// - Readiness checks are skipped when this version of the resource's desired state has already become ready
	// - Readiness checks are skipped when the resource hasn't changed since the last check
	// - Readiness defaults to true if no checks are given
	logger.Info("evaluating resource readiness")
	status := resource.State()
	if status == nil || status.Ready == nil {
		readiness, ok := resource.ReadinessChecks.EvalOptionally(ctx, &apiv1.Composition{}, current)
		if ok {
			ready = &readiness.ReadyTime
			logger.Info("resource is ready", "readyTime", ready)
		}
	} else {
		ready = status.Ready
	}

	logger.Info("creating resource snapshot")
	snap, err = resource.Snapshot(ctx, comp, current)
	if err != nil {
		logger.Error(err, "failed to create resource snapshot")
		return nil, nil, nil, false, fmt.Errorf("failed to create resource snapshot: %w", err)
	}
	if status := snap.OverrideStatus(); len(status) > 0 {
		logger = logger.WithValues("overrideStatus", status)
		ctx = logr.NewContext(ctx, logger)
	}

	modified, err = c.reconcileSnapshot(ctx, comp, prev, snap, current)
	if err != nil {
		logger.Error(err, "failed to reconcile resource snapshot")
	}
	return snap, current, ready, modified, err
}

func (c *Controller) reconcileSnapshot(ctx context.Context, comp *apiv1.Composition, prev *resource.Resource, res *resource.Snapshot, current *unstructured.Unstructured) (bool, error) {
	if res.Disable {
		return false, nil
	}

	logger := logr.FromContextOrDiscard(ctx)
	start := time.Now()
	defer func() {
		reconciliationLatency.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if res.Deleted() {
		if current == nil || current.GetDeletionTimestamp() != nil || (res.Orphan && comp.DeletionTimestamp != nil) || (comp.Labels != nil && comp.Labels["eno.azure.io/symphony-deleting"] == "true") {
			return false, nil // already deleted - nothing to do
		}

		reconciliationActions.WithLabelValues("delete").Inc()
		err := c.upstreamClient.Delete(ctx, current)
		if err != nil {
			return true, client.IgnoreNotFound(fmt.Errorf("deleting resource: %w", err))
		}
		logger.Info("deleted resource")
		return true, nil
	}

	patchJson, isPatch, err := res.Patch()
	if err != nil {
		return false, fmt.Errorf("building patch: %w", err)
	}
	if isPatch && current == nil {
		logger.Info("resource doesn't exist - skipping patch")
		return false, nil
	}

	// Create the resource when it doesn't exist, should exist, and wouldn't be created later by server-side apply
	if current == nil && (res.DisableUpdates || res.Replace || c.disableSSA) {
		reconciliationActions.WithLabelValues("create").Inc()
		err := c.upstreamClient.Create(ctx, res.Unstructured())
		if err != nil {
			return false, fmt.Errorf("creating resource: %w", err)
		}
		logger.Info("created resource")
		return true, nil
	}

	if res.DisableUpdates {
		return false, nil
	}

	// Apply Eno patches
	if isPatch {
		if patchJson == nil {
			return false, nil // patch is empty
		}

		reconciliationActions.WithLabelValues("patch").Inc()
		updated := current.DeepCopy()
		err := c.upstreamClient.Patch(ctx, updated, client.RawPatch(types.JSONPatchType, patchJson))
		if err != nil {
			logger.Error(err, "failed to patch resource")
			return false, fmt.Errorf("applying patch: %w", err)
		}
		if updated.GetResourceVersion() == current.GetResourceVersion() {
			logger.Info("resource didn't change after patch")
			return false, nil
		}
		logger.Info("patched resource", "resourceVersion", updated.GetResourceVersion())
		return true, nil
	}

	// Dry-run the update to see if it's needed
	if !c.disableSSA {
		// Before the dry-run, normalize conflicting field managers to "eno" to prevent SSA validation errors
		// caused by multiple managers owning overlapping fields. When managers are renamed to "eno", the
		// subsequent SSA Apply will treat eno as the sole owner and automatically merge the managedFields
		// entries into a single consolidated entry for eno.
		if current != nil && len(c.migratingFieldManagers) > 0 {
			wasModified, err := resource.NormalizeConflictingManagers(ctx, current, c.migratingFieldManagers)
			if err != nil {
				return false, fmt.Errorf("normalize conflicting manager failed: %w", err)
			}
			if wasModified {
				logger.Info("Normalized conflicting managers to eno")
				err = c.upstreamClient.Update(ctx, current, client.FieldOwner("eno"))
				if err != nil {
					return false, fmt.Errorf("normalizing managedFields failed: %w", err)
				}
				// refetch the current before apply dry-run
				current, err = c.getCurrent(ctx, res.Resource)
				if err != nil {
					logger.Error(err, "failed to get current resource after eno ownership migration")
					return false, fmt.Errorf("re-fetching after normalizing manager failed: %w", err)
				}

				logger.Info("Successfully normalized field managers to eno")
			}
		}
		dryRun, err := c.update(ctx, comp, prev, res, current, true)
		if err != nil {
			logger.Error(err, "dry-run update failed.")
			return false, fmt.Errorf("dry-run applying update: %w", err)
		}
		if resource.Compare(dryRun, current) {
			logger.Info("resource insync, no operation needed")
			return false, nil // in sync
		}

		// When using server side apply, make sure we haven't lost any managedFields metadata.
		// Eno should always remove fields that are no longer set by the synthesizer, even if another client messed with managedFields.
		if current != nil && prev != nil && !res.Replace {
			snap, err := prev.SnapshotWithOverrides(ctx, comp, current, res.Resource)
			if err != nil {
				logger.Error(err, "failed to get SnapshotWithOverrides")
				return false, fmt.Errorf("snapshotting previous version: %w", err)
			}
			dryRunPrev := snap.Unstructured()
			err = c.upstreamClient.Patch(ctx, dryRunPrev, client.Apply, client.ForceOwnership, client.FieldOwner("eno"), client.DryRunAll)
			if err != nil {
				logger.Error(err, "faile dto get managedFields values")
				return false, fmt.Errorf("getting managed fields values for previous version: %w", err)
			}

			merged, fields, modified := resource.MergeEnoManagedFields(dryRunPrev.GetManagedFields(), current.GetManagedFields(), dryRun.GetManagedFields())
			if modified {
				current.SetManagedFields(merged)

				err := c.upstreamClient.Update(ctx, current, client.FieldOwner("eno"))
				if err != nil {
					logger.Error(err, "failed to update managed fields for resource")
					return false, fmt.Errorf("updating managed fields metadata: %w", err)
				}
				logger.Info("corrected drift in managed fields metadata", "fields", fields)
				return true, nil
			}
		}
	}

	// Do the actual non-dryrun update
	reconciliationActions.WithLabelValues("apply").Inc()
	updated, err := c.update(ctx, comp, prev, res, current, false)
	if err != nil {
		logger.Error(err, "failed when applying update")
		return false, fmt.Errorf("applying update: %w", err)
	}
	if current != nil && updated.GetResourceVersion() == current.GetResourceVersion() {
		logger.Info("resource didn't change after update")
		return false, nil
	}
	if current != nil {
		logger = logger.WithValues("oldResourceVersion", current.GetResourceVersion())
	}
	logger.Info("applied resource")
	return true, nil
}

func (c *Controller) update(ctx context.Context, comp *apiv1.Composition, previous *resource.Resource, resource *resource.Snapshot, current *unstructured.Unstructured, dryrun bool) (updated *unstructured.Unstructured, err error) {
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
		patch, err = buildNonStrategicPatch(ctx, comp, previous, current)
		if err != nil {
			return nil, fmt.Errorf("building patch: %w", err)
		}
	} else {
		patch = client.Apply
		opts = append(opts, client.ForceOwnership, client.FieldOwner("eno"))
	}

	err = c.upstreamClient.Patch(ctx, updated, patch, opts...)
	return
}

func buildNonStrategicPatch(ctx context.Context, comp *apiv1.Composition, previous *resource.Resource, current *unstructured.Unstructured) (client.Patch, error) {
	var from *unstructured.Unstructured
	if previous == nil {
		from = &unstructured.Unstructured{Object: map[string]any{}}
	} else {
		snap, err := previous.Snapshot(ctx, comp, current)
		if err != nil {
			return nil, err
		}
		from = snap.Unstructured()
	}
	return client.MergeFrom(from), nil
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

func (c *Controller) requeue(logger logr.Logger, resource *resource.Snapshot, ready *metav1.Time) (ctrl.Result, error) {
	pendingForegroundDeletion := (resource != nil && resource.Deleted() && !resource.Disable && resource.ForegroundDeletion)

	if ready == nil || pendingForegroundDeletion {
		return ctrl.Result{RequeueAfter: wait.Jitter(c.readinessPollInterval, 0.1)}, nil
	}

	if resource == nil || (resource.Deleted() && !resource.Disable) || resource.ReconcileInterval == nil {
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

func patchResourceError(ctx context.Context, err error) flowcontrol.StatusPatchFn {
	return func(rs *apiv1.ResourceState) *apiv1.ResourceState {
		logger := logr.FromContextOrDiscard(ctx)
		str := summarizeError(ctx, err)
		logger.Info("summarized resource error for status patch", "errorSummary", str)
		if rs != nil && (rs.Reconciled || (rs.ReconciliationError != nil && *rs.ReconciliationError == str)) {
			logger.Info("no resource error status change detected")
			return nil
		}
		return &apiv1.ResourceState{
			Reconciled:          rs.Reconciled,
			Ready:               rs.Ready,
			Deleted:             rs.Deleted,
			ReconciliationError: &str,
		}
	}
}

func summarizeError(ctx context.Context, err error) string {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("summarizing error for status patch", "error", err)
	if err == nil {
		logger.Info("no error provided, returning empty summary")
		return ""
	}

	statusErr := &errors.StatusError{}
	if !goerrors.As(err, &statusErr) {
		logger.Info("non-StatusError, returning full error message")
		return err.Error()
	}
	status := statusErr.Status()

	// SSA is sloppy with the status codes
	if spl := strings.SplitAfter(status.Message, "failed to create typed patch object"); len(spl) > 1 {
		reason := strings.TrimSpace(spl[1])
		logger.Info("SSA patch error, extracting reason", "reason", reason)
		return reason
	}

	switch status.Reason {
	case metav1.StatusReasonBadRequest,
		metav1.StatusReasonNotAcceptable,
		metav1.StatusReasonRequestEntityTooLarge,
		metav1.StatusReasonMethodNotAllowed,
		metav1.StatusReasonGone,
		metav1.StatusReasonForbidden,
		metav1.StatusReasonUnauthorized:
		logger.Info("status reason suitable for summarization", "reason", status.Reason, "message", status.Message)
		return status.Message

	default:
		logger.Info("status reason not suitable for summarization, returning full error message", "reason", status.Reason)
		return err.Error()
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
