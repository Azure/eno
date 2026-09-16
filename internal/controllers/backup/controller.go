package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	enocel "github.com/Azure/eno/internal/cel"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"github.com/google/cel-go/cel"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	reasonInProgress        = "BackupInProgress"
	reasonNotNeeded         = "NotNeeded"
	reasonDisabled          = "BackupOperatorNotEnabled"
	reasonInventoryNotFound = "InventoryNotFound"
	reasonInventoryGetError = "InventoryGetError"
	reasonInventoryInvalid  = "InventoryInvalid"
	reasonSliceReadError    = "CurrentSynthesisResourceSliceError"
	reasonSliceWriteError   = "ErrUpdateResourceSlice"
	reasonFinished          = "FinishedTombstoneRecovery"
)

var errSuperseded = errors.New("backup operation superseded")

type Options struct {
	Enabled              bool
	Downstream           *rest.Config
	ResourceFilter       cel.Program
	CompositionNamespace string
	CompositionSelector  labels.Selector
}

type operation struct {
	uid           types.UID
	synthesisUUID string
	lineage       string
	inventory     *inventorySnapshot
	recordingDone bool
}

type backupController struct {
	client               client.Client
	reader               client.Reader
	downstream           client.Client
	resourceFilter       cel.Program
	enabled              bool
	operations           map[types.NamespacedName]*operation
	compositionNamespace string
	compositionSelector  labels.Selector

	deletedCompositionsMu sync.Mutex
	deletedCompositions   map[types.NamespacedName]*apiv1.Composition
}

type jitteredRateLimiter struct {
	workqueue.TypedRateLimiter[reconcile.Request]
}

func (r *jitteredRateLimiter) When(req reconcile.Request) time.Duration {
	return wait.Jitter(r.TypedRateLimiter.When(req), 0.2)
}

func NewController(mgr ctrl.Manager, opts Options) error {
	c := &backupController{
		client:               mgr.GetClient(),
		reader:               mgr.GetAPIReader(),
		resourceFilter:       opts.ResourceFilter,
		enabled:              opts.Enabled,
		operations:           map[types.NamespacedName]*operation{},
		compositionNamespace: opts.CompositionNamespace,
		compositionSelector:  opts.CompositionSelector,
	}
	if opts.Enabled {
		config := opts.Downstream
		if config == nil {
			config = mgr.GetConfig()
		}
		config = rest.CopyConfig(config)
		if config.Timeout == 0 || config.Timeout > 10*time.Second {
			config.Timeout = 10 * time.Second
		}
		var err error
		c.downstream, err = client.New(config, client.Options{Scheme: mgr.GetScheme()})
		if err != nil {
			return fmt.Errorf("constructing backup downstream client: %w", err)
		}
		gc, err := newInventoryGC(mgr, config, c.ownsComposition)
		if err != nil {
			return err
		}
		if err := mgr.Add(gc); err != nil {
			return fmt.Errorf("registering inventory garbage collection: %w", err)
		}
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("backupController").
		For(&apiv1.Composition{}).
		Owns(&apiv1.ResourceSlice{}).
		WatchesRawSource(source.Kind(mgr.GetCache(), &apiv1.Composition{}, c.newDeletedCompositionHandler())).
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
	if handled, err := c.retireDeletedComposition(ctx, req.NamespacedName); handled {
		return c.result(ctx, ctrl.Result{Requeue: true}, err)
	}
	comp, err := c.filterCompositionByResourceFilter(ctx, req.NamespacedName)
	if err != nil {
		return c.result(ctx, ctrl.Result{}, err)
	}
	if comp == nil {
		delete(c.operations, req.NamespacedName)
		return ctrl.Result{}, nil
	}
	// Deletion reuses existing slices and must not wait for backup, even if recovery is unfinished.
	if comp.DeletionTimestamp != nil {
		delete(c.operations, req.NamespacedName)
		logger.Info("composition is deleting; skipping recovery and inventory recording", "compositionUID", comp.UID,
			"synthesisUUID", comp.Status.GetCurrentSynthesisUUID())
		return ctrl.Result{}, nil
	}
	syn := comp.Status.CurrentSynthesis
	if syn == nil || syn.Synthesized == nil {
		delete(c.operations, req.NamespacedName)
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("compositionUID", comp.UID, "compositionGeneration", comp.Generation, "synthesisUUID", syn.UUID,
		"synthesizerName", comp.Spec.Synthesizer.Name, "operationID", comp.GetAzureOperationID(), "operationOrigin", comp.GetAzureOperationOrigin())
	ctx = logr.NewContext(ctx, logger)
	if syn.UUID == "" {
		return c.result(ctx, ctrl.Result{}, fmt.Errorf("published synthesis has no UUID"))
	}
	op := c.operations[req.NamespacedName]
	lineage := inventoryLineage(comp)
	if op == nil || op.uid != comp.UID || op.synthesisUUID != syn.UUID || op.lineage != lineage {
		op = &operation{uid: comp.UID, synthesisUUID: syn.UUID, lineage: lineage}
		c.operations[req.NamespacedName] = op
	}

	if !syn.TombstoneRecoveryComplete() {
		ctx = logr.NewContext(ctx, logger.WithValues("operation", "recoveryPreparation"))
		if !c.enabled {
			result, err := c.finishRecovery(ctx, comp, reasonDisabled, "", nil)
			return c.result(ctx, result, err)
		}
		if !syn.TombstoneRecoveryRequired {
			result, err := c.finishRecovery(ctx, comp, reasonNotNeeded, "", nil)
			return c.result(ctx, result, err)
		}
		result, err := c.prepare(ctx, comp, op)
		return c.result(ctx, result, err)
	}

	op.inventory = nil
	if !c.enabled || syn.Ready == nil || op.recordingDone {
		return ctrl.Result{}, nil
	}
	switch syn.TombstoneRecoveryFinished.Reason {
	case reasonInventoryGetError, reasonInventoryInvalid:
		logger.Info("inventory recording deferred because historical inventory is unresolved", "operation", "inventoryRecording",
			"reason", syn.TombstoneRecoveryFinished.Reason)
		return ctrl.Result{}, nil
	}
	ctx = logr.NewContext(ctx, logger.WithValues("operation", "inventoryRecording", "inventoryConfigMap", inventoryName(comp, syn.UUID)))
	err = c.record(ctx, comp)
	if err == nil {
		op.recordingDone = true
	}
	return c.result(ctx, ctrl.Result{}, err)
}

func (c *backupController) result(ctx context.Context, result ctrl.Result, err error) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)
	if errors.Is(err, errSuperseded) {
		logger.Info("abandoning obsolete backup work", "reason", err.Error())
		return ctrl.Result{Requeue: true}, nil
	}
	if err != nil {
		logger.Error(err, "backup operation failed")
	}
	return result, err
}

func (c *backupController) filterCompositionByResourceFilter(ctx context.Context, key types.NamespacedName) (*apiv1.Composition, error) {
	cached := &apiv1.Composition{}
	if err := c.client.Get(ctx, key, cached); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	comp := &apiv1.Composition{}
	if err := c.reader.Get(ctx, key, comp); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	// The cached lookup honors the manager's watch scope; reject a stale routing decision.
	if comp.UID != cached.UID || !reflect.DeepEqual(comp.Labels, cached.Labels) || !reflect.DeepEqual(comp.Annotations, cached.Annotations) {
		return nil, fmt.Errorf("%w: composition routing changed", errSuperseded)
	}
	matches, err := c.ownsComposition(ctx, comp)
	if err != nil {
		return nil, err
	}

	if !matches {
		logr.FromContextOrDiscard(ctx).V(1).Info("composition is outside the backup resource filter")
		return nil, nil
	}
	return comp, nil
}

func (c *backupController) ownsComposition(ctx context.Context, comp *apiv1.Composition) (bool, error) {
	if c.compositionNamespace != "" && c.compositionNamespace != comp.Namespace {
		return false, nil
	}
	if c.compositionSelector != nil && !c.compositionSelector.Matches(labels.Set(comp.Labels)) {
		return false, nil
	}
	return enocel.MayMatchComposition(ctx, c.resourceFilter, comp)
}

func (c *backupController) current(ctx context.Context, expected *apiv1.Composition, requireReady bool) (*apiv1.Composition, error) {
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
	if comp.UID != expected.UID || syn == nil || old == nil || syn.UUID != old.UUID ||
		comp.Spec.Synthesizer.Name != expected.Spec.Synthesizer.Name ||
		!reflect.DeepEqual(comp.Labels, expected.Labels) || !reflect.DeepEqual(comp.Annotations, expected.Annotations) ||
		!reflect.DeepEqual(comp.OwnerReferences, expected.OwnerReferences) ||
		!reflect.DeepEqual(syn.ResourceSlices, old.ResourceSlices) ||
		!reflect.DeepEqual(syn.TombstoneRecoveryFinished, old.TombstoneRecoveryFinished) ||
		syn.TombstoneRecoveryRequired != old.TombstoneRecoveryRequired {
		logr.FromContextOrDiscard(ctx).Info("observed obsolete backup operation", "originalSynthesisUUID", expected.Status.GetCurrentSynthesisUUID(),
			"currentSynthesisUUID", comp.Status.GetCurrentSynthesisUUID())
		return nil, fmt.Errorf("%w: composition or recovery decision changed (current synthesis %q)", errSuperseded, comp.Status.GetCurrentSynthesisUUID())
	}
	if requireReady && syn.Ready == nil {
		return nil, fmt.Errorf("%w: synthesis is no longer eligible for inventory recording", errSuperseded)
	}
	return comp, nil
}

func recoveryStatus(comp *apiv1.Composition) apiv1.TombstoneRecoveryStatus {
	syn := comp.Status.CurrentSynthesis
	if status := syn.TombstoneRecoveryFinished; status != nil && status.SynthesisUUID == syn.UUID {
		return *status
	}
	return apiv1.TombstoneRecoveryStatus{SynthesisUUID: syn.UUID}
}

func (c *backupController) prepare(ctx context.Context, comp *apiv1.Composition, op *operation) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("operation", "recoveryPreparation")
	ctx = logr.NewContext(ctx, logger)
	status := recoveryStatus(comp)
	if status.Reason == "" {
		status.Reason = reasonInProgress
		after := comp.DeepCopy()
		after.Status.CurrentSynthesis.TombstoneRecoveryFinished = &status
		logger.Info("starting tombstone recovery preparation")
		return ctrl.Result{Requeue: true}, c.patchStatus(ctx, comp, after)
	}

	if op.inventory == nil {
		logger.Info("reading downstream inventory", "lineage", inventoryLineage(comp))
		_, snapshot, readErr := c.readInventories(ctx, comp)
		var err error
		comp, err = c.current(ctx, comp, false)
		if err != nil {
			return ctrl.Result{}, err
		}
		if readErr != nil {
			logger.Error(readErr, "failed to read downstream inventory")
			var invalid *invalidInventoryError
			if errors.As(readErr, &invalid) {
				return c.finishRecovery(ctx, comp, reasonInventoryInvalid, readErr.Error(), nil)
			}
			return ctrl.Result{}, c.preparationError(ctx, comp, reasonInventoryGetError, readErr)
		}
		if snapshot == nil {
			logger.Info("no downstream inventory found", "lineage", inventoryLineage(comp))
			return c.finishRecovery(ctx, comp, reasonInventoryNotFound, "", nil)
		}
		op.inventory = snapshot
	}

	slices, err := c.loadCurrent(ctx, comp)
	if err != nil {
		return ctrl.Result{}, c.preparationError(ctx, comp, reasonSliceReadError, err)
	}
	tombstones, err := missingTombstones(ctx, comp, slices, op.inventory, c.resourceFilter)
	if err != nil {
		return ctrl.Result{}, c.preparationError(ctx, comp, reasonSliceReadError, err)
	}
	comp, err = c.current(ctx, comp, false)
	if err != nil {
		return ctrl.Result{}, err
	}
	refs, err := c.writeTombstones(ctx, comp, slices, tombstones)
	if err != nil {
		return ctrl.Result{}, c.preparationError(ctx, comp, reasonSliceWriteError, err)
	}
	comp, err = c.current(ctx, comp, false)
	if err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("recovery tombstones persisted", "tombstoneCount", len(tombstones), "overflowSliceCount", len(refs))
	result, err := c.finishRecovery(ctx, comp, reasonFinished, "", refs)
	if err != nil && len(refs) > 0 {
		return ctrl.Result{}, c.preparationError(ctx, comp, reasonSliceWriteError, err)
	}
	return result, err
}

func (c *backupController) loadCurrent(ctx context.Context, comp *apiv1.Composition) ([]apiv1.ResourceSlice, error) {
	var slices []apiv1.ResourceSlice
	seen := map[string]bool{}
	for i, ref := range comp.Status.CurrentSynthesis.ResourceSlices {
		if ref == nil || ref.Name == "" {
			return nil, fmt.Errorf("current ResourceSlice reference %d has no name", i)
		}
		if seen[ref.Name] {
			return nil, fmt.Errorf("current ResourceSlice reference %q is duplicated", ref.Name)
		}
		seen[ref.Name] = true
		slice := apiv1.ResourceSlice{}
		if err := c.reader.Get(ctx, types.NamespacedName{Namespace: comp.Namespace, Name: ref.Name}, &slice); err != nil {
			return nil, fmt.Errorf("reading current ResourceSlice %q: %w", ref.Name, err)
		}
		owner := metav1.GetControllerOf(&slice)
		if owner == nil || owner.UID != comp.UID || owner.Kind != "Composition" || owner.Name != comp.Name {
			return nil, fmt.Errorf("current ResourceSlice %q is not owned by this Composition", ref.Name)
		}
		if slice.DeletionTimestamp != nil {
			return nil, fmt.Errorf("current ResourceSlice %q is being deleted", ref.Name)
		}
		slices = append(slices, slice)
	}
	return slices, nil
}

func (c *backupController) preparationError(ctx context.Context, expected *apiv1.Composition, reason string, cause error) error {
	if errors.Is(cause, errSuperseded) {
		return cause
	}
	comp, err := c.current(ctx, expected, false)
	if err != nil {
		return err
	}
	logger := logr.FromContextOrDiscard(ctx)
	logger.Error(cause, "tombstone recovery preparation failed", "reason", reason)
	status := recoveryStatus(comp)
	status.Status, status.Reason, status.Message = false, reason, cause.Error()
	after := comp.DeepCopy()
	after.Status.CurrentSynthesis.TombstoneRecoveryFinished = &status
	if err := c.patchStatus(ctx, comp, after); err != nil {
		logger.Error(err, "failed to persist recovery preparation error")
		return errors.Join(cause, err)
	}
	return cause
}

func (c *backupController) finishRecovery(ctx context.Context, comp *apiv1.Composition, reason, message string, refs []*apiv1.ResourceSliceRef) (ctrl.Result, error) {
	status := recoveryStatus(comp)
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
		return ctrl.Result{}, err
	}
	logr.FromContextOrDiscard(ctx).Info("tombstone recovery preparation finished", "operation", "recoveryPreparation", "reason", reason, "message", message)
	return ctrl.Result{Requeue: true}, nil
}

func (c *backupController) readInventories(ctx context.Context, comp *apiv1.Composition) ([]corev1.ConfigMap, *inventorySnapshot, error) {
	list := &corev1.ConfigMapList{}
	if err := c.downstream.List(ctx, list, client.InNamespace(inventoryNamespace),
		client.MatchingLabels{inventoryLineageLabel: inventoryLineage(comp)}); err != nil {
		return nil, nil, fmt.Errorf("listing downstream inventory ConfigMaps: %w", err)
	}
	snapshot, err := selectInventory(comp, list.Items)
	return list.Items, snapshot, err
}

func (c *backupController) record(ctx context.Context, comp *apiv1.Composition) error {
	logger := logr.FromContextOrDiscard(ctx).WithValues("operation", "inventoryRecording")
	ctx = logr.NewContext(ctx, logger)
	items, latest, err := c.readInventories(ctx, comp)
	if err != nil {
		return err
	}
	comp, err = c.current(ctx, comp, true)
	if err != nil {
		return err
	}
	syn := comp.Status.CurrentSynthesis
	if latest != nil && latest.Data.Synthesized.After(syn.Synthesized.Time) {
		logger.Info("retaining newer inventory instead of recording an older synthesis", "inventoryConfigMap", latest.Object.Name)
		return nil
	}
	if latest != nil && latest.Data.Synthesized.Equal(syn.Synthesized) && latest.Data.SynthesisUUID != syn.UUID {
		return fmt.Errorf("inventory timestamp is ambiguous with synthesis %q", latest.Data.SynthesisUUID)
	}
	slices, err := c.loadCurrent(ctx, comp)
	if err != nil {
		return err
	}
	if latest != nil {
		missing, err := missingTombstones(ctx, comp, slices, latest, c.resourceFilter)
		if err != nil {
			return err
		}
		if len(missing) > 0 {
			logger.Info("retaining inventory with unresolved historical identities", "inventoryConfigMap", latest.Object.Name, "unresolvedCount", len(missing))
			return nil
		}
	}
	snapshot, err := makeInventory(ctx, comp, slices, c.resourceFilter)
	if err != nil {
		return err
	}
	logger = logger.WithValues("inventoryConfigMap", snapshot.Object.Name)
	ctx = logr.NewContext(ctx, logger)
	comp, err = c.current(ctx, comp, true)
	if err != nil {
		return err
	}
	var persisted *inventorySnapshot
	for _, item := range items {
		if item.Name == snapshot.Object.Name {
			persisted, err = decodeInventory(comp, item)
			if err != nil {
				return err
			}
			break
		}
	}
	if persisted == nil {
		persisted, err = c.createInventory(ctx, comp, snapshot)
		if err != nil {
			return err
		}
	}
	if !inventoriesMatch(persisted, snapshot) {
		return fmt.Errorf("existing inventory ConfigMap %q does not match the intended snapshot", snapshot.Object.Name)
	}
	logger.Info("inventory snapshot persisted")
	comp, err = c.current(ctx, comp, true)
	if err != nil {
		return err
	}
	if comp.Status.CurrentSynthesis.TombstoneRecoveryRequired && comp.Status.CurrentSynthesis.TombstoneRecoveryFinished.Reason == reasonFinished {
		after := comp.DeepCopy()
		after.Status.CurrentSynthesis.TombstoneRecoveryRequired = false
		if err := c.patchStatus(ctx, comp, after); err != nil {
			return err
		}
		comp = after
		logger.Info("cleared tombstone recovery requirement after inventory persistence")
	}

	for _, item := range items {
		old, err := decodeInventory(comp, item)
		if err != nil {
			return err
		}
		if !old.Data.Synthesized.Before(&snapshot.Data.Synthesized) {
			continue
		}
		comp, err = c.current(ctx, comp, true)
		if err != nil {
			return err
		}
		err = c.downstream.Delete(ctx, &item, &client.Preconditions{UID: &item.UID, ResourceVersion: &item.ResourceVersion})
		if err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to clean up superseded inventory", "operation", "inventoryCleanup", "deletedInventoryConfigMap", item.Name)
			return fmt.Errorf("deleting superseded inventory ConfigMap %q: %w", item.Name, err)
		}
		logger.Info("superseded inventory cleaned up", "operation", "inventoryCleanup", "deletedInventoryConfigMap", item.Name)
	}
	return nil
}

func (c *backupController) createInventory(ctx context.Context, comp *apiv1.Composition, snapshot *inventorySnapshot) (*inventorySnapshot, error) {
	if err := c.downstream.Create(ctx, &snapshot.Object); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating inventory ConfigMap %q: %w", snapshot.Object.Name, err)
		}
		existing := corev1.ConfigMap{}
		if err := c.downstream.Get(ctx, client.ObjectKeyFromObject(&snapshot.Object), &existing); err != nil {
			return nil, fmt.Errorf("reading existing inventory ConfigMap %q: %w", snapshot.Object.Name, err)
		}
		return decodeInventory(comp, existing)
	}
	return snapshot, nil
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
	if old.TombstoneRecoveryRequired != next.TombstoneRecoveryRequired {
		ops = append(ops, statusPatch{Op: "add", Path: "/status/currentSynthesis/tombstoneRecoveryRequired", Value: next.TombstoneRecoveryRequired})
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
