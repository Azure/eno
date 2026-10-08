package tombstonerecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	reasonNotNeeded         = "NotNeeded"
	reasonInventoryNotFound = "InventoryNotFound"
	reasonInventoryGetError = "InventoryGetError"
	reasonInventoryInvalid  = "InventoryInvalid"
	reasonSliceReadError    = "CurrentSynthesisResourceSliceError"
	reasonSliceWriteError   = "ErrUpdateResourceSlice"
	reasonFinished          = "FinishedTombstoneRecovery"
)

var errSuperseded = errors.New("tombstone recovery operation superseded")

type compositionOperation string

const (
	compositionOperationNone      compositionOperation = ""
	compositionOperationRecover   compositionOperation = "recoverMissingTombstones"
	compositionOperationNotNeeded compositionOperation = "recordRecoveryNotNeeded"
	compositionOperationRecord    compositionOperation = "recordInventory"
)

type Options struct {
	Enabled             bool
	Namespace           string
	CompositionSelector labels.Selector
	Downstream          *rest.Config
}

type tombstoneRecoveryController struct {
	client     client.Client
	reader     client.Reader
	downstream client.Client
}

type jitteredRateLimiter struct {
	workqueue.TypedRateLimiter[reconcile.Request]
}

func (r *jitteredRateLimiter) When(req reconcile.Request) time.Duration {
	return wait.Jitter(r.TypedRateLimiter.When(req), 0.2)
}

func NewController(mgr ctrl.Manager, opts Options) error {
	if !opts.Enabled {
		return nil
	}
	if opts.Namespace == "" {
		return fmt.Errorf("tombstone recovery namespace is required")
	}
	recoveryCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme:            mgr.GetScheme(),
		Mapper:            mgr.GetRESTMapper(),
		HTTPClient:        mgr.GetHTTPClient(),
		DefaultNamespaces: map[string]cache.Config{opts.Namespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&apiv1.Composition{}: {Label: opts.CompositionSelector},
		},
	})
	if err != nil {
		return fmt.Errorf("constructing tombstone recovery namespace cache: %w", err)
	}
	if err := mgr.Add(recoveryCache); err != nil {
		return fmt.Errorf("registering tombstone recovery namespace cache: %w", err)
	}
	upstream, err := client.New(mgr.GetConfig(), client.Options{
		Scheme:     mgr.GetScheme(),
		Mapper:     mgr.GetRESTMapper(),
		HTTPClient: mgr.GetHTTPClient(),
		Cache:      &client.CacheOptions{Reader: recoveryCache},
	})
	if err != nil {
		return fmt.Errorf("constructing tombstone recovery upstream client: %w", err)
	}
	c := &tombstoneRecoveryController{
		client: upstream,
		reader: mgr.GetAPIReader(),
	}
	config := opts.Downstream
	if config == nil {
		config = mgr.GetConfig()
	}
	config = rest.CopyConfig(config)
	if config.Timeout == 0 || config.Timeout > 10*time.Second {
		config.Timeout = 10 * time.Second
	}
	c.downstream, err = client.New(config, client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return fmt.Errorf("constructing tombstone recovery downstream client: %w", err)
	}
	slice := &metav1.PartialObjectMetadata{}
	slice.SetGroupVersionKind(apiv1.SchemeGroupVersion.WithKind("ResourceSlice"))
	return ctrl.NewControllerManagedBy(mgr).
		Named("tombstoneRecoveryController").
		WatchesRawSource(source.Kind(recoveryCache, &apiv1.Composition{}, &handler.TypedEnqueueRequestForObject[*apiv1.Composition]{})).
		WatchesRawSource(source.Kind(recoveryCache, slice, handler.TypedEnqueueRequestForOwner[*metav1.PartialObjectMetadata](
			mgr.GetScheme(), mgr.GetRESTMapper(), &apiv1.Composition{}, handler.OnlyControllerOwner()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			RateLimiter:             &jitteredRateLimiter{workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()},
		}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "tombstoneRecoveryController")).
		Complete(c)
}

func (c *tombstoneRecoveryController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("compositionName", req.Name, "compositionNamespace", req.Namespace)
	ctx = logr.NewContext(ctx, logger)
	comp := &apiv1.Composition{}
	err := c.client.Get(ctx, req.NamespacedName, comp)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		logger.Error(err, "failed to get composition")
		return ctrl.Result{}, err
	}
	// Deletion reuses existing slices and must not wait for tombstone recovery.
	if comp.DeletionTimestamp != nil {
		logger.Info("composition is deleting; skipping recovery and inventory recording", "compositionUID", comp.UID,
			"synthesisUUID", comp.Status.GetCurrentSynthesisUUID())
		return ctrl.Result{}, nil
	}
	syn := comp.Status.CurrentSynthesis
	if syn == nil || syn.Synthesized == nil {
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("compositionUID", comp.UID, "compositionGeneration", comp.Generation, "synthesisUUID", syn.UUID,
		"synthesizerName", comp.Spec.Synthesizer.Name, "operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)
	if !comp.RecoveryEnabled() {
		logger.Info("composition has not opted into recovery")
		return ctrl.Result{}, nil
	}
	if syn.UUID == "" {
		logger.Error(err, "invalid synthesis")
		return ctrl.Result{}, fmt.Errorf("published synthesis has no UUID")
	}

	operation := nextCompositionOperation(comp)
	ctx = logr.NewContext(ctx, logger.WithValues("operation", operation))
	switch operation {
	case compositionOperationRecover:
		err = c.recoverMissingTombstones(ctx, comp)
	case compositionOperationNotNeeded:
		// Recording waits for a later event containing this persisted decision.
		err = c.recordRecoveryDecision(ctx, comp, reasonNotNeeded, "", nil)
	case compositionOperationRecord:
		err = c.recordInventory(ctx, comp)
	}

	if errors.Is(err, errSuperseded) {
		logger.Info("abandoning obsolete tombstone recovery work", "reason", err.Error())
		return ctrl.Result{Requeue: true}, nil
	}
	if err != nil {
		logger.Error(err, "tombstone recovery operation failed")
	}
	return ctrl.Result{}, err
}

// nextCompositionOperation selects one idempotent operation without performing side effects.
func nextCompositionOperation(comp *apiv1.Composition) compositionOperation {
	if comp == nil || comp.Status.CurrentSynthesis == nil || comp.Status.CurrentSynthesis.Synthesized == nil {
		return compositionOperationNone
	}
	syn := comp.Status.CurrentSynthesis
	if !syn.IsTombstoneRecoveryFinished() {
		if syn.TombstoneRecoveryRequired {
			return compositionOperationRecover
		}
		return compositionOperationNotNeeded
	}
	if syn.Ready != nil {
		return compositionOperationRecord
	}
	return compositionOperationNone
}

func (c *tombstoneRecoveryController) getCurrentComposition(ctx context.Context, expected *apiv1.Composition, requireReady bool) (*apiv1.Composition, error) {
	comp := &apiv1.Composition{}
	if err := c.reader.Get(ctx, client.ObjectKeyFromObject(expected), comp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: composition no longer exists", errSuperseded)
		}
		return nil, err
	}
	// Deletion can start before the Composition controller changes the synthesis UUID.
	if comp.DeletionTimestamp != nil {
		return nil, fmt.Errorf("%w: composition is deleting", errSuperseded)
	}
	if !comp.RecoveryEnabled() {
		return nil, fmt.Errorf("%w: composition recovery is disabled", errSuperseded)
	}
	syn, old := comp.Status.CurrentSynthesis, expected.Status.CurrentSynthesis
	if syn == nil || old == nil || syn.UUID != old.UUID {
		logr.FromContextOrDiscard(ctx).Info("observed obsolete tombstone recovery operation", "originalSynthesisUUID", expected.Status.GetCurrentSynthesisUUID(),
			"currentSynthesisUUID", comp.Status.GetCurrentSynthesisUUID())
		return nil, fmt.Errorf("%w: synthesis changed (current synthesis %q)", errSuperseded, comp.Status.GetCurrentSynthesisUUID())
	}
	if requireReady && (syn.Ready == nil || !syn.IsTombstoneRecoveryFinished() ||
		syn.Synthesized == nil || !syn.Synthesized.Equal(old.Synthesized) ||
		!reflect.DeepEqual(syn.ResourceSlices, old.ResourceSlices)) {
		return nil, fmt.Errorf("%w: synthesis is no longer eligible for inventory recording", errSuperseded)
	}
	return comp, nil
}

func getOrCreateRecoveryStatus(comp *apiv1.Composition) apiv1.TombstoneRecoveryStatus {
	syn := comp.Status.CurrentSynthesis
	if status := syn.TombstoneRecoveryStatus; status != nil && status.SynthesisUUID == syn.UUID {
		return *status
	}
	return apiv1.TombstoneRecoveryStatus{SynthesisUUID: syn.UUID}
}

func (c *tombstoneRecoveryController) recoverMissingTombstones(ctx context.Context, comp *apiv1.Composition) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("reading downstream inventory", "lineage", inventoryLineage(comp))
	items, readErr := c.readInventories(ctx, comp)
	var snapshot []corev1.Secret
	if readErr == nil {
		snapshot, readErr = selectInventory(items)
	}
	var resources []inventoryResource
	if readErr == nil && len(snapshot) > 0 {
		resources, readErr = decodeInventorySnapshot(comp, snapshot)
	}
	if readErr != nil {
		logger.Error(readErr, "failed to read downstream inventory")
		var invalid *invalidInventoryError
		if errors.As(readErr, &invalid) {
			return c.recordRecoveryDecision(ctx, comp, reasonInventoryInvalid, readErr.Error(), nil)
		}
		return c.recordTombstoneRecoveryError(ctx, comp, reasonInventoryGetError, readErr)
	}
	if len(snapshot) == 0 {
		logger.Info("no downstream inventory found", "lineage", inventoryLineage(comp))
		return c.recordRecoveryDecision(ctx, comp, reasonInventoryNotFound, "", nil)
	}

	slices, err := c.loadCurrentSynthesisResourceSlices(ctx, comp)
	if err != nil {
		return c.recordTombstoneRecoveryError(ctx, comp, reasonSliceReadError, err)
	}
	tombstones, err := missingTombstones(slices, resources)
	if err != nil {
		return c.recordTombstoneRecoveryError(ctx, comp, reasonSliceReadError, err)
	}
	refs, err := c.writeTombstones(ctx, comp, slices, tombstones)
	if err != nil {
		return c.recordTombstoneRecoveryError(ctx, comp, reasonSliceWriteError, err)
	}
	logger.Info("recovery tombstones persisted", "tombstoneCount", len(tombstones), "overflowSliceCount", len(refs))
	err = c.recordRecoveryDecision(ctx, comp, reasonFinished, "", refs)
	if err != nil && len(refs) > 0 {
		return c.recordTombstoneRecoveryError(ctx, comp, reasonSliceWriteError, err)
	}
	return err
}

func (c *tombstoneRecoveryController) loadCurrentSynthesisResourceSlices(ctx context.Context, comp *apiv1.Composition) ([]apiv1.ResourceSlice, error) {
	var slices []apiv1.ResourceSlice
	for i, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		if ref == nil || ref.Name == "" {
			return nil, fmt.Errorf("current ResourceSlice reference %d has no name", i)
		}
		slice := apiv1.ResourceSlice{}
		if err := c.reader.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: ref.Name}, &slice); err != nil {
			return nil, fmt.Errorf("reading current ResourceSlice %q: %w", ref.Name, err)
		}
		if slice.DeletionTimestamp != nil {
			return nil, fmt.Errorf("current ResourceSlice %q is being deleted", ref.Name)
		}
		slices = append(slices, slice)
	}
	return slices, nil
}

func (c *tombstoneRecoveryController) recordTombstoneRecoveryError(ctx context.Context, comp *apiv1.Composition, reason string, cause error) error {
	if errors.Is(cause, errSuperseded) {
		return cause
	}
	logger := logr.FromContextOrDiscard(ctx)
	logger.Error(cause, "tombstone recovery preparation failed", "reason", reason)
	status := getOrCreateRecoveryStatus(comp)
	status.Status, status.Reason, status.Message = false, reason, cause.Error()
	after := comp.DeepCopy()
	after.Status.CurrentSynthesis.TombstoneRecoveryStatus = &status
	if err := c.patchStatus(ctx, comp, after); err != nil {
		logger.Error(err, "failed to persist recovery preparation error")
		return errors.Join(cause, err)
	}
	return cause
}

func (c *tombstoneRecoveryController) recordRecoveryDecision(ctx context.Context, comp *apiv1.Composition, reason, message string, refs []*apiv1.ResourceSliceRef) error {
	status := getOrCreateRecoveryStatus(comp)
	status.Status, status.Reason, status.Message = true, reason, message
	after := comp.DeepCopy()
	after.Status.CurrentSynthesis.TombstoneRecoveryStatus = &status
	seen := map[string]bool{}
	for _, ref := range after.Status.CurrentSynthesis.ResourceSlices {
		if ref != nil {
			seen[ref.Name] = true
		}
	}
	for _, ref := range refs {
		if !seen[ref.Name] {
			after.Status.CurrentSynthesis.ResourceSlices = append(after.Status.CurrentSynthesis.ResourceSlices, ref)
			seen[ref.Name] = true
		}
	}
	if err := c.patchStatus(ctx, comp, after); err != nil {
		return err
	}
	logr.FromContextOrDiscard(ctx).Info("tombstone recovery preparation finished", "operation", "recoveryPreparation", "reason", reason, "message", message)
	return nil
}

func (c *tombstoneRecoveryController) readInventories(ctx context.Context, comp *apiv1.Composition) ([]corev1.Secret, error) {
	list := &corev1.SecretList{}
	if err := c.downstream.List(ctx, list, client.InNamespace(inventoryNamespace),
		client.MatchingLabels{inventoryLineageLabel: inventoryLineage(comp)}); err != nil {
		return nil, fmt.Errorf("listing downstream inventory Secrets: %w", err)
	}
	return list.Items, nil
}

type statusPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (c *tombstoneRecoveryController) patchStatus(ctx context.Context, before, after *apiv1.Composition) error {
	if before.DeletionTimestamp != nil {
		return fmt.Errorf("%w: composition is deleting", errSuperseded)
	}
	old, next := before.Status.CurrentSynthesis, after.Status.CurrentSynthesis
	// The resourceVersion precondition also protects against deletion starting after our read.
	ops := []statusPatch{
		{Op: "test", Path: "/metadata/uid", Value: before.UID},
		{Op: "test", Path: "/metadata/resourceVersion", Value: before.ResourceVersion},
		{Op: "test", Path: "/status/currentSynthesis/uuid", Value: old.UUID},
	}
	if !reflect.DeepEqual(old.TombstoneRecoveryStatus, next.TombstoneRecoveryStatus) {
		ops = append(ops, statusPatch{Op: "add", Path: "/status/currentSynthesis/tombstoneRecoveryStatus", Value: next.TombstoneRecoveryStatus})
	}
	if !reflect.DeepEqual(old.ResourceSlices, next.ResourceSlices) {
		ops = append(ops, statusPatch{Op: "add", Path: "/status/currentSynthesis/resourceSlices", Value: next.ResourceSlices})
	}
	if len(ops) == 3 {
		return nil
	}
	data, err := json.Marshal(ops)
	if err != nil {
		return fmt.Errorf("encoding recovery status patch: %w", err)
	}
	if err := c.client.Status().Patch(ctx, after, client.RawPatch(types.JSONPatchType, data)); err != nil {
		return fmt.Errorf("persisting recovery status: %w", err)
	}
	return nil
}
