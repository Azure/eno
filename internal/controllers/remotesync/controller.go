// Package remotesync implements the RemoteSyncController which syncs resources
// from remote clusters to the local cluster as InputMirror resources.
//
// ARCHITECTURE:
// This controller runs in the eno-reconciler and uses the reconciler's existing
// remote client (configured via --remote-kubeconfig) to watch and sync resources:
// 1. The controller is initialized with a pre-configured remote REST config
// 2. It sets up dynamic informers on the remote cluster for each Symphony's refs
// 3. When a watched resource changes, the informer triggers a reconcile
// 4. The reconciler syncs the changed resource to the corresponding InputMirror on the local cluster
//
// SECURITY CONSIDERATIONS:
// - Remote credentials are managed by the reconciler's --remote-kubeconfig flag
// - No credentials stored in Symphony specs
// - REST client has timeouts to prevent resource exhaustion
// - Only specified resource types can be synced (no arbitrary access)
package remotesync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// ConditionTypeSynced indicates whether the InputMirror has been successfully synced
	ConditionTypeSynced = "Synced"

	// FallbackSyncInterval is used when watches fail or as a safety net for missed events.
	// Watches provide real-time updates, so this is only a fallback.
	FallbackSyncInterval = 30 * time.Minute

	// WatchResyncPeriod is how often the informer re-lists all objects as a consistency check
	WatchResyncPeriod = 10 * time.Minute

	// FinalizerName is the finalizer added to InputMirrors
	FinalizerName = "eno.azure.io/remote-sync"

	// Client timeout settings for security
	remoteClientTimeout = 30 * time.Second
	remoteClientQPS     = 5
	remoteClientBurst   = 10

	// maxSyncConcurrency limits parallel remote resource fetches per Symphony.
	// This prevents overwhelming the remote cluster's API server while still
	// providing significant speedup over sequential syncing.
	// With 100 refs at ~50ms each: sequential = 5s, parallel (10) = 500ms
	maxSyncConcurrency = 10
)

// AllowedSyncKinds defines which resource kinds can be synced from remote.
// This is a security control to prevent syncing sensitive resources.
var AllowedSyncKinds = map[schema.GroupKind]bool{
	{Group: "", Kind: "ConfigMap"}: true,
	// Add other allowed kinds here as needed
	// Explicitly NOT allowing: Secret, ServiceAccount, etc.
}

// remoteWatcher manages watch connections to the remote cluster.
// It maintains dynamic informers for each resource type being watched.
type remoteWatcher struct {
	mu sync.RWMutex

	// Dynamic client and informer factory for the remote cluster
	dynamicClient   dynamic.Interface
	informerFactory dynamicinformer.DynamicSharedInformerFactory
	stopCh          chan struct{}

	// Track which GVRs we're watching
	watchedGVRs map[schema.GroupVersionResource]struct{}

	// Reference to the controller for enqueuing reconciles
	controller *Controller
}

// Controller reconciles Symphonies with remote resource refs, syncing resources
// from remote clusters to InputMirror resources on the local cluster.
type Controller struct {
	client client.Client
	scheme *runtime.Scheme

	// remoteWatcher is the shared watcher for the remote cluster
	// initialized once from the remote REST config passed to NewController
	remoteWatcher *remoteWatcher

	// allowedKinds can be overridden for testing
	allowedKinds map[schema.GroupKind]bool

	// eventChan receives events from informer callbacks to trigger reconciles
	eventChan chan event.TypedGenericEvent[*apiv1.Symphony]
}

// NewController creates a new RemoteSyncController and registers it with the manager.
// The remoteConfig is the REST config for the remote cluster (typically from --remote-kubeconfig).
// If remoteConfig is nil, the controller will not sync any resources.
func NewController(mgr ctrl.Manager, remoteConfig *rest.Config) error {
	// Create buffered event channel to receive watch events from informers
	eventChan := make(chan event.TypedGenericEvent[*apiv1.Symphony], 100)

	c := &Controller{
		client:       mgr.GetClient(),
		scheme:       mgr.GetScheme(),
		allowedKinds: AllowedSyncKinds,
		eventChan:    eventChan,
	}

	// Initialize the shared remote watcher if config is provided
	if remoteConfig != nil {
		watcher, err := newRemoteWatcher(remoteConfig, c)
		if err != nil {
			return fmt.Errorf("creating remote watcher: %w", err)
		}
		c.remoteWatcher = watcher
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Symphony{}).
		Owns(&apiv1.InputMirror{}).
		WatchesRawSource(source.Channel(eventChan, &handler.TypedEnqueueRequestForObject[*apiv1.Symphony]{})).
		WithLogConstructor(manager.NewLogConstructor(mgr, "remoteSyncController")).
		Complete(c)
}

// newRemoteWatcher creates a new remote watcher from the given REST config
func newRemoteWatcher(config *rest.Config, controller *Controller) (*remoteWatcher, error) {
	// Create dynamic client for informers
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	// Create informer factory
	stopCh := make(chan struct{})
	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, WatchResyncPeriod)

	return &remoteWatcher{
		dynamicClient:   dynamicClient,
		informerFactory: informerFactory,
		stopCh:          stopCh,
		watchedGVRs:     make(map[schema.GroupVersionResource]struct{}),
		controller:      controller,
	}, nil
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	// Skip if no remote watcher configured (no --remote-kubeconfig)
	if c.remoteWatcher == nil {
		return ctrl.Result{}, nil
	}

	symphony := &apiv1.Symphony{}
	if err := c.client.Get(ctx, req.NamespacedName, symphony); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger = logger.WithValues(
		"symphonyName", symphony.Name,
		"symphonyNamespace", symphony.Namespace,
	)
	ctx = logr.NewContext(ctx, logger)

	// Skip if no remote resource refs defined
	if len(symphony.Spec.RemoteResourceRefs) == 0 {
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if symphony.DeletionTimestamp != nil {
		// InputMirrors will be garbage collected via owner references
		return ctrl.Result{}, nil
	}

	// Ensure informers are set up for all resource refs
	if err := c.remoteWatcher.ensureInformers(ctx, symphony); err != nil {
		logger.Error(err, "failed to setup informers")
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Sync all remote resource refs in parallel with bounded concurrency
	// This handles the initial sync and any changes detected by watches
	c.syncRemoteResourcesParallel(ctx, symphony, c.remoteWatcher)

	// Clean up InputMirrors for refs that no longer exist
	if err := c.cleanupOrphanedMirrors(ctx, symphony); err != nil {
		logger.Error(err, "failed to cleanup orphaned mirrors")
	}

	// With watches, we only need periodic reconciles as a fallback safety net
	return ctrl.Result{RequeueAfter: FallbackSyncInterval}, nil
}

// ensureInformers sets up informers for all resource refs in the symphony
func (w *remoteWatcher) ensureInformers(ctx context.Context, symphony *apiv1.Symphony) error {
	logger := logr.FromContextOrDiscard(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()

	// Track which GVRs we need
	neededGVRs := make(map[schema.GroupVersionResource][]apiv1.RemoteResourceRef)
	for _, ref := range symphony.Spec.RemoteResourceRefs {
		gvr := schema.GroupVersionResource{
			Group:    ref.Resource.Group,
			Version:  ref.Resource.Version,
			Resource: pluralize(ref.Resource.Kind),
		}
		neededGVRs[gvr] = append(neededGVRs[gvr], ref)
	}

	// Set up informers for new GVRs
	for gvr, refs := range neededGVRs {
		if _, exists := w.watchedGVRs[gvr]; exists {
			continue
		}

		logger.V(1).Info("setting up informer for GVR", "gvr", gvr.String())

		informer := w.informerFactory.ForResource(gvr).Informer()

		// Add event handler that triggers reconcile on changes
		_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				w.enqueueReconcile(ctx, symphony, refs, obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				w.enqueueReconcile(ctx, symphony, refs, newObj)
			},
			DeleteFunc: func(obj interface{}) {
				w.enqueueReconcile(ctx, symphony, refs, obj)
			},
		})
		if err != nil {
			return fmt.Errorf("adding event handler for %s: %w", gvr.String(), err)
		}

		w.watchedGVRs[gvr] = struct{}{}
	}

	// Start the informer factory (idempotent if already started)
	w.informerFactory.Start(w.stopCh)

	// Wait for caches to sync
	synced := w.informerFactory.WaitForCacheSync(w.stopCh)
	for gvr, ok := range synced {
		if !ok {
			logger.Error(nil, "failed to sync informer cache", "gvr", gvr.String())
		}
	}

	return nil
}

// enqueueReconcile checks if the object matches any refs and enqueues a reconcile if so
func (w *remoteWatcher) enqueueReconcile(ctx context.Context, symphony *apiv1.Symphony, refs []apiv1.RemoteResourceRef, obj interface{}) {
	logger := logr.FromContextOrDiscard(ctx)

	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		// Handle DeletedFinalStateUnknown
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			u, ok = tombstone.Obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
		} else {
			return
		}
	}

	// Check if this object matches any of our refs
	for _, ref := range refs {
		if matchesRef(u, ref) {
			logger.V(1).Info("remote resource changed, enqueueing reconcile",
				"resource", u.GetName(),
				"namespace", u.GetNamespace(),
				"key", ref.Key,
			)
			// Send event through the channel to trigger reconcile
			// Use non-blocking send to avoid blocking informer callbacks
			select {
			case w.controller.eventChan <- event.TypedGenericEvent[*apiv1.Symphony]{
				Object: symphony,
			}:
			default:
				logger.V(1).Info("event channel full, reconcile will happen on next poll")
			}
			return
		}
	}
}

// matchesRef checks if an unstructured object matches a remote resource ref
func matchesRef(obj *unstructured.Unstructured, ref apiv1.RemoteResourceRef) bool {
	return obj.GetName() == ref.Resource.Name &&
		obj.GetNamespace() == ref.Resource.Namespace
}

// pluralize converts a Kind to its plural resource name (simple heuristic)
func pluralize(kind string) string {
	// Use strings.ToLower to properly lowercase the entire string (first letter only is insufficient for multi-word kinds like configMap)
	lower := strings.ToLower(kind)
	if lower[len(lower)-1] == 's' {
		return lower + "es"
	}
	if lower[len(lower)-1] == 'y' {
		return lower[:len(lower)-1] + "ies"
	}
	return lower + "s"
}

// getResource fetches a resource from the remote cluster using the dynamic client
func (w *remoteWatcher) getResource(ctx context.Context, ref apiv1.RemoteResourceRef) (*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    ref.Resource.Group,
		Version:  ref.Resource.Version,
		Resource: pluralize(ref.Resource.Kind),
	}

	var obj *unstructured.Unstructured
	var err error

	if ref.Resource.Namespace != "" {
		obj, err = w.dynamicClient.Resource(gvr).Namespace(ref.Resource.Namespace).Get(ctx, ref.Resource.Name, metav1.GetOptions{})
	} else {
		obj, err = w.dynamicClient.Resource(gvr).Get(ctx, ref.Resource.Name, metav1.GetOptions{})
	}

	return obj, err
}

// syncResult holds the result of syncing a single remote resource
type syncResult struct {
	key string
	err error
}

// syncRemoteResourcesParallel syncs all remote resource refs in parallel with bounded concurrency.
// This reduces reconcile latency from O(n * latency) to O(n/concurrency * latency).
// For example, with 100 refs at ~50ms each: sequential = 5s, parallel (10) = ~500ms.
func (c *Controller) syncRemoteResourcesParallel(
	ctx context.Context,
	symphony *apiv1.Symphony,
	watcher *remoteWatcher,
) {
	logger := logr.FromContextOrDiscard(ctx)
	refs := symphony.Spec.RemoteResourceRefs

	if len(refs) == 0 {
		return
	}

	// Use a semaphore to limit concurrent overlay API calls
	sem := make(chan struct{}, maxSyncConcurrency)
	results := make(chan syncResult, len(refs))

	// Use errgroup for structured concurrency, but we don't fail fast on errors
	// since we want to sync as many refs as possible
	g, ctx := errgroup.WithContext(ctx)

	for _, ref := range refs {
		ref := ref // capture loop variable
		g.Go(func() error {
			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- syncResult{key: ref.Key, err: ctx.Err()}
				return nil
			}

			err := c.syncRemoteResource(ctx, symphony, watcher, ref)
			results <- syncResult{key: ref.Key, err: err}
			return nil // Don't propagate errors - we handle them individually
		})
	}

	// Wait for all goroutines to complete
	_ = g.Wait()
	close(results)

	// Process results
	var successCount, failCount int

	for result := range results {
		if result.err != nil {
			logger.Error(result.err, "failed to sync remote resource", "key", result.key)
			failCount++
			continue
		}
		successCount++
	}

	logger.V(1).Info("completed parallel remote sync",
		"total", len(refs),
		"success", successCount,
		"failed", failCount,
	)
}

// syncRemoteResource syncs a single remote resource to an InputMirror
func (c *Controller) syncRemoteResource(
	ctx context.Context,
	symphony *apiv1.Symphony,
	watcher *remoteWatcher,
	ref apiv1.RemoteResourceRef,
) error {
	logger := logr.FromContextOrDiscard(ctx).WithValues("key", ref.Key, "resourceName", ref.Resource.Name)

	// SECURITY: Validate the resource kind is allowed to be synced
	gk := schema.GroupKind{Group: ref.Resource.Group, Kind: ref.Resource.Kind}
	if !c.allowedKinds[gk] {
		return fmt.Errorf("security: resource kind %q is not allowed to be synced from remote", gk.String())
	}

	// Fetch from remote using the watcher's dynamic client
	obj, err := watcher.getResource(ctx, ref)
	if err != nil {
		if errors.IsNotFound(err) && ref.Optional {
			logger.V(1).Info("optional remote resource not found, skipping")
			// Update InputMirror to reflect missing state
			return c.updateMirrorMissing(ctx, symphony, ref)
		}
		return fmt.Errorf("getting remote resource: %w", err)
	}

	// Create/Update InputMirror
	mirrorName := inputMirrorName(symphony.Name, ref.Key)
	mirror := &apiv1.InputMirror{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mirrorName,
			Namespace: symphony.Namespace,
		},
	}

	result, err := controllerutil.CreateOrUpdate(ctx, c.client, mirror, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(symphony, mirror, c.scheme); err != nil {
			return err
		}

		// Update spec
		mirror.Spec.Key = ref.Key
		mirror.Spec.SymphonyRef = corev1.LocalObjectReference{Name: symphony.Name}
		mirror.Spec.SourceResource = ref.Resource

		return nil
	})

	if err != nil {
		return fmt.Errorf("creating/updating InputMirror: %w", err)
	}

	// Update status separately - CreateOrUpdate only updates spec, not status subresource
	rawData, err := json.Marshal(obj.Object)
	if err != nil {
		return fmt.Errorf("marshaling resource data: %w", err)
	}
	mirror.Status.Data = &runtime.RawExtension{Raw: rawData}
	mirror.Status.LastSyncTime = &metav1.Time{Time: time.Now()}
	mirror.Status.SyncGeneration = obj.GetResourceVersion()

	// Update conditions
	setSyncedCondition(mirror, true, "SyncSuccess", "Successfully synced from remote cluster")

	if err := c.client.Status().Update(ctx, mirror); err != nil {
		return fmt.Errorf("updating InputMirror status: %w", err)
	}

	logger.V(1).Info("synced remote resource", "result", result, "mirrorName", mirrorName)
	return nil
}

// updateMirrorMissing updates the InputMirror to reflect that the source resource is missing
func (c *Controller) updateMirrorMissing(
	ctx context.Context,
	symphony *apiv1.Symphony,
	ref apiv1.RemoteResourceRef,
) error {
	mirrorName := inputMirrorName(symphony.Name, ref.Key)
	mirror := &apiv1.InputMirror{}
	err := c.client.Get(ctx, types.NamespacedName{Name: mirrorName, Namespace: symphony.Namespace}, mirror)
	if errors.IsNotFound(err) {
		// No mirror exists, nothing to update
		return nil
	}
	if err != nil {
		return err
	}

	// Update condition to reflect missing state
	setSyncedCondition(mirror, false, "SourceNotFound", "Optional source resource not found in remote cluster")
	mirror.Status.Data = nil

	return c.client.Status().Update(ctx, mirror)
}

// cleanupOrphanedMirrors removes InputMirrors for refs that no longer exist in the Symphony
func (c *Controller) cleanupOrphanedMirrors(ctx context.Context, symphony *apiv1.Symphony) error {
	logger := logr.FromContextOrDiscard(ctx)

	// List all InputMirrors owned by this Symphony
	mirrors := &apiv1.InputMirrorList{}
	if err := c.client.List(ctx, mirrors,
		client.InNamespace(symphony.Namespace),
		client.MatchingFields{"spec.symphonyRef.name": symphony.Name},
	); err != nil {
		// If the index isn't set up, fall back to filtering manually
		if err := c.client.List(ctx, mirrors, client.InNamespace(symphony.Namespace)); err != nil {
			return err
		}
	}

	// Build set of expected mirror names
	expected := make(map[string]struct{})
	for _, ref := range symphony.Spec.RemoteResourceRefs {
		expected[inputMirrorName(symphony.Name, ref.Key)] = struct{}{}
	}

	// Delete orphaned mirrors
	for _, mirror := range mirrors.Items {
		// Check if owned by this symphony
		if mirror.Spec.SymphonyRef.Name != symphony.Name {
			continue
		}
		if _, ok := expected[mirror.Name]; !ok {
			logger.V(1).Info("deleting orphaned InputMirror", "mirrorName", mirror.Name)
			if err := c.client.Delete(ctx, &mirror); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// inputMirrorName generates the name for an InputMirror
func inputMirrorName(symphonyName, key string) string {
	return fmt.Sprintf("%s-%s", symphonyName, key)
}

// setSyncedCondition updates the Synced condition on an InputMirror
func setSyncedCondition(mirror *apiv1.InputMirror, synced bool, reason, message string) {
	status := metav1.ConditionFalse
	if synced {
		status = metav1.ConditionTrue
	}

	now := metav1.Now()
	condition := metav1.Condition{
		Type:               ConditionTypeSynced,
		Status:             status,
		ObservedGeneration: mirror.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	}

	// Find and update existing condition or append
	for i, c := range mirror.Status.Conditions {
		if c.Type == ConditionTypeSynced {
			if c.Status != condition.Status {
				mirror.Status.Conditions[i] = condition
			} else {
				// Only update reason/message, keep transition time
				mirror.Status.Conditions[i].Reason = reason
				mirror.Status.Conditions[i].Message = message
				mirror.Status.Conditions[i].ObservedGeneration = mirror.Generation
			}
			return
		}
	}
	mirror.Status.Conditions = append(mirror.Status.Conditions, condition)
}
