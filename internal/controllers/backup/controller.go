package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	enocel "github.com/Azure/eno/internal/cel"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"github.com/google/cel-go/cel"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

var errSuperseded = errors.New("backup operation superseded")

type Options struct {
	Enabled             bool
	Namespace           string
	CompositionSelector labels.Selector
	Downstream          *rest.Config
	ResourceFilter      cel.Program
}

type backupController struct {
	client         client.Client
	reader         client.Reader
	downstream     client.Client
	resourceFilter cel.Program
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
		return fmt.Errorf("backup namespace is required")
	}
	backupCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme:            mgr.GetScheme(),
		Mapper:            mgr.GetRESTMapper(),
		HTTPClient:        mgr.GetHTTPClient(),
		DefaultNamespaces: map[string]cache.Config{opts.Namespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&apiv1.Composition{}: {Label: opts.CompositionSelector},
		},
	})
	if err != nil {
		return fmt.Errorf("constructing backup namespace cache: %w", err)
	}
	if err := mgr.Add(backupCache); err != nil {
		return fmt.Errorf("registering backup namespace cache: %w", err)
	}
	upstream, err := client.New(mgr.GetConfig(), client.Options{
		Scheme:     mgr.GetScheme(),
		Mapper:     mgr.GetRESTMapper(),
		HTTPClient: mgr.GetHTTPClient(),
		Cache:      &client.CacheOptions{Reader: backupCache},
	})
	if err != nil {
		return fmt.Errorf("constructing backup upstream client: %w", err)
	}
	c := &backupController{
		client:         upstream,
		reader:         mgr.GetAPIReader(),
		resourceFilter: opts.ResourceFilter,
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
		return fmt.Errorf("constructing backup downstream client: %w", err)
	}
	slice := &metav1.PartialObjectMetadata{}
	slice.SetGroupVersionKind(apiv1.SchemeGroupVersion.WithKind("ResourceSlice"))
	return ctrl.NewControllerManagedBy(mgr).
		Named("backupController").
		WatchesRawSource(source.Kind(backupCache, &apiv1.Composition{}, &handler.TypedEnqueueRequestForObject[*apiv1.Composition]{})).
		WatchesRawSource(source.Kind(backupCache, slice, handler.TypedEnqueueRequestForOwner[*metav1.PartialObjectMetadata](
			mgr.GetScheme(), mgr.GetRESTMapper(), &apiv1.Composition{}, handler.OnlyControllerOwner()))).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 1,
			RateLimiter:             &jitteredRateLimiter{workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()},
		}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "backupController")).
		Complete(c)
}

func (c *backupController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
	// Deletion reuses existing slices and must not wait for backup, even if recovery is unfinished.
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
	matches, err := c.matchesComposition(ctx, comp)
	if err != nil {
		logger.Error(err, "failed to evaluate composition resource filter")
		return ctrl.Result{}, err
	}
	if !matches {
		logger.Info("composition is outside the backup resource filter")
		return ctrl.Result{}, nil
	}
	if syn.UUID == "" {
		logger.Error(err, "invalid synthesis")
		return ctrl.Result{}, fmt.Errorf("published synthesis has no UUID")
	}

	// Inventory must use the persisted recovery decision and slice references from a subsequent event.
	if !syn.TombstoneRecoveryComplete() {
		logger = logger.WithValues("operation", "recoveryPreparation")
		ctx = logr.NewContext(ctx, logger)
		if syn.TombstoneRecoveryRequired {
			err = c.tombstoneRecovery(ctx, comp)
		} else {
			err = c.markTombstoneRecoveryFinished(ctx, comp, reasonNotNeeded, "", nil)
		}
	}

	if errors.Is(err, errSuperseded) {
		logger.Info("abandoning obsolete backup work", "reason", err.Error())
		return ctrl.Result{Requeue: true}, nil
	}
	if err != nil {
		logger.Error(err, "backup operation failed")
	}
	return ctrl.Result{}, err
}

func (c *backupController) matchesComposition(ctx context.Context, comp *apiv1.Composition) (bool, error) {
	if c.resourceFilter == nil {
		return true, nil
	}
	// This check uses Composition metadata only; self has no resource labels.
	self := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{}}}
	result, err := enocel.Eval(ctx, c.resourceFilter, comp, self, nil)
	if err != nil {
		return false, fmt.Errorf("evaluating composition resource filter: %w", err)
	}
	matches, ok := result.Value().(bool)
	if !ok {
		return false, fmt.Errorf("resource filter expression must return a boolean, got %T", result.Value())
	}
	return matches, nil
}

func (c *backupController) getCurrentComposition(ctx context.Context, expected *apiv1.Composition, requireReady bool) (*apiv1.Composition, error) {
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
	syn, old := comp.Status.CurrentSynthesis, expected.Status.CurrentSynthesis
	if syn == nil || old == nil || syn.UUID != old.UUID {
		logr.FromContextOrDiscard(ctx).Info("observed obsolete backup operation", "originalSynthesisUUID", expected.Status.GetCurrentSynthesisUUID(),
			"currentSynthesisUUID", comp.Status.GetCurrentSynthesisUUID())
		return nil, fmt.Errorf("%w: synthesis changed (current synthesis %q)", errSuperseded, comp.Status.GetCurrentSynthesisUUID())
	}
	if requireReady && syn.Ready == nil {
		return nil, fmt.Errorf("%w: synthesis is no longer eligible for inventory recording", errSuperseded)
	}
	return comp, nil
}

func getOrCreateRecoveryStatus(comp *apiv1.Composition) apiv1.TombstoneRecoveryStatus {
	syn := comp.Status.CurrentSynthesis
	if status := syn.TombstoneRecoveryFinished; status != nil && status.SynthesisUUID == syn.UUID {
		return *status
	}
	return apiv1.TombstoneRecoveryStatus{SynthesisUUID: syn.UUID}
}

func (c *backupController) tombstoneRecovery(ctx context.Context, comp *apiv1.Composition) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("reading downstream inventory", "lineage", inventoryLineage(comp))
	items, readErr := c.readInventories(ctx, comp)
	var configMap *corev1.ConfigMap
	if readErr == nil {
		configMap, readErr = selectInventory(items)
	}
	var resources []inventoryResource
	if readErr == nil && configMap != nil {
		resources, readErr = decodeInventorySnapshot(comp, *configMap)
	}
	if readErr != nil {
		logger.Error(readErr, "failed to read downstream inventory")
		var invalid *invalidInventoryError
		if errors.As(readErr, &invalid) {
			return c.markTombstoneRecoveryFinished(ctx, comp, reasonInventoryInvalid, readErr.Error(), nil)
		}
		return c.recordTombstoneRecoveryError(ctx, comp, reasonInventoryGetError, readErr)
	}
	if configMap == nil {
		logger.Info("no downstream inventory found", "lineage", inventoryLineage(comp))
		return c.markTombstoneRecoveryFinished(ctx, comp, reasonInventoryNotFound, "", nil)
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
	err = c.markTombstoneRecoveryFinished(ctx, comp, reasonFinished, "", refs)
	if err != nil && len(refs) > 0 {
		return c.recordTombstoneRecoveryError(ctx, comp, reasonSliceWriteError, err)
	}
	return err
}

func (c *backupController) loadCurrentSynthesisResourceSlices(ctx context.Context, comp *apiv1.Composition) ([]apiv1.ResourceSlice, error) {
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

func (c *backupController) recordTombstoneRecoveryError(ctx context.Context, comp *apiv1.Composition, reason string, cause error) error {
	if errors.Is(cause, errSuperseded) {
		return cause
	}
	logger := logr.FromContextOrDiscard(ctx)
	logger.Error(cause, "tombstone recovery preparation failed", "reason", reason)
	status := getOrCreateRecoveryStatus(comp)
	status.Status, status.Reason, status.Message = false, reason, cause.Error()
	after := comp.DeepCopy()
	after.Status.CurrentSynthesis.TombstoneRecoveryFinished = &status
	if err := c.patchStatus(ctx, comp, after); err != nil {
		logger.Error(err, "failed to persist recovery preparation error")
		return errors.Join(cause, err)
	}
	return cause
}

func (c *backupController) markTombstoneRecoveryFinished(ctx context.Context, comp *apiv1.Composition, reason, message string, refs []*apiv1.ResourceSliceRef) error {
	status := getOrCreateRecoveryStatus(comp)
	status.Status, status.Reason, status.Message = true, reason, message
	after := comp.DeepCopy()
	after.Status.CurrentSynthesis.TombstoneRecoveryFinished = &status
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

func (c *backupController) readInventories(ctx context.Context, comp *apiv1.Composition) ([]corev1.ConfigMap, error) {
	list := &corev1.ConfigMapList{}
	if err := c.downstream.List(ctx, list, client.InNamespace(inventoryNamespace),
		client.MatchingLabels{inventoryLineageLabel: inventoryLineage(comp)}); err != nil {
		return nil, fmt.Errorf("listing downstream inventory ConfigMaps: %w", err)
	}
	return list.Items, nil
}

type statusPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (c *backupController) patchStatus(ctx context.Context, before, after *apiv1.Composition) error {
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
	if !reflect.DeepEqual(old.TombstoneRecoveryFinished, next.TombstoneRecoveryFinished) {
		ops = append(ops, statusPatch{Op: "add", Path: "/status/currentSynthesis/tombstoneRecoveryFinished", Value: next.TombstoneRecoveryFinished})
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
