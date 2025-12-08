// Package overlaysync implements the OverlaySyncController which syncs resources
// from overlay clusters to the underlay as InputMirror resources.
//
// SECURITY CONSIDERATIONS:
// - Overlay credentials are stored in Secrets and never logged
// - Secret access is restricted to the Symphony's namespace by default
// - REST client has timeouts to prevent resource exhaustion
// - Cached clients are invalidated on credential rotation
// - Only specified resource types can be synced (no arbitrary access)
package overlaysync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/manager"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// ConditionTypeSynced indicates whether the InputMirror has been successfully synced
	ConditionTypeSynced = "Synced"

	// DefaultSyncInterval is the default interval for re-syncing overlay resources
	DefaultSyncInterval = 5 * time.Minute

	// FinalizerName is the finalizer added to InputMirrors
	FinalizerName = "eno.azure.io/overlay-sync"

	// Client timeout settings for security
	overlayClientTimeout = 30 * time.Second
	overlayClientQPS     = 5
	overlayClientBurst   = 10
)

// AllowedSyncKinds defines which resource kinds can be synced from overlay.
// This is a security control to prevent syncing sensitive resources.
var AllowedSyncKinds = map[schema.GroupKind]bool{
	{Group: "", Kind: "ConfigMap"}: true,
	// Add other allowed kinds here as needed
	// Explicitly NOT allowing: Secret, ServiceAccount, etc.
}

// overlayClient holds a cached client for an overlay cluster
type overlayClient struct {
	client         client.Client
	createdAt      time.Time
	credentialHash string // Hash of credentials to detect rotation
}

// Controller reconciles Symphonies with overlay resource refs, syncing resources
// from overlay clusters to InputMirror resources on the underlay.
type Controller struct {
	client client.Client
	scheme *runtime.Scheme

	// overlayClients caches overlay cluster clients keyed by symphony namespace/name
	overlayClients sync.Map

	// clientCacheTTL determines how long overlay clients are cached
	clientCacheTTL time.Duration

	// allowedKinds can be overridden for testing
	allowedKinds map[schema.GroupKind]bool
}

// NewController creates a new OverlaySyncController and registers it with the manager.
func NewController(mgr ctrl.Manager) error {
	c := &Controller{
		client:         mgr.GetClient(),
		scheme:         mgr.GetScheme(),
		clientCacheTTL: 10 * time.Minute,
		allowedKinds:   AllowedSyncKinds,
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Symphony{}).
		Owns(&apiv1.InputMirror{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "overlaySyncController")).
		Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logr.FromContextOrDiscard(ctx)

	symphony := &apiv1.Symphony{}
	if err := c.client.Get(ctx, req.NamespacedName, symphony); err != nil {
		if errors.IsNotFound(err) {
			// Symphony deleted, overlay clients will be cleaned up by GC
			c.overlayClients.Delete(req.String())
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger = logger.WithValues(
		"symphonyName", symphony.Name,
		"symphonyNamespace", symphony.Namespace,
	)
	ctx = logr.NewContext(ctx, logger)

	// Skip if no overlay resource refs defined
	if len(symphony.Spec.OverlayResourceRefs) == 0 {
		return ctrl.Result{}, nil
	}

	// Skip if no overlay credentials provided
	if symphony.Spec.OverlayCredentials == nil {
		logger.V(1).Info("symphony has overlay resource refs but no credentials, skipping")
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if symphony.DeletionTimestamp != nil {
		// InputMirrors will be garbage collected via owner references
		c.overlayClients.Delete(req.String())
		return ctrl.Result{}, nil
	}

	// Get or create overlay client
	overlayClient, err := c.getOrCreateOverlayClient(ctx, symphony)
	if err != nil {
		logger.Error(err, "failed to create overlay client")
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Sync each overlay resource ref
	var minRequeue time.Duration
	for _, ref := range symphony.Spec.OverlayResourceRefs {
		requeue, err := c.syncOverlayResource(ctx, symphony, overlayClient, ref)
		if err != nil {
			logger.Error(err, "failed to sync overlay resource", "key", ref.Key)
			// Continue with other refs
		}
		if requeue > 0 && (minRequeue == 0 || requeue < minRequeue) {
			minRequeue = requeue
		}
	}

	// Clean up InputMirrors for refs that no longer exist
	if err := c.cleanupOrphanedMirrors(ctx, symphony); err != nil {
		logger.Error(err, "failed to cleanup orphaned mirrors")
	}

	if minRequeue > 0 {
		return ctrl.Result{RequeueAfter: minRequeue}, nil
	}
	return ctrl.Result{RequeueAfter: DefaultSyncInterval}, nil
}

// hashCredentials creates a SHA256 hash of credential data for change detection.
// This allows detecting credential rotation without storing the credentials.
func hashCredentials(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// getOrCreateOverlayClient gets a cached overlay client or creates a new one.
// Security: Credentials are never logged, client has timeouts, cache invalidates on rotation.
func (c *Controller) getOrCreateOverlayClient(ctx context.Context, symphony *apiv1.Symphony) (client.Client, error) {
	logger := logr.FromContextOrDiscard(ctx)
	key := fmt.Sprintf("%s/%s", symphony.Namespace, symphony.Name)

	// Get the kubeconfig secret first to check for credential rotation
	creds := symphony.Spec.OverlayCredentials
	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{
		Name:      creds.SecretRef.Name,
		Namespace: creds.SecretRef.Namespace,
	}

	// SECURITY: Only allow accessing secrets in the Symphony's namespace
	// This prevents cross-namespace credential access
	if secretKey.Namespace == "" {
		secretKey.Namespace = symphony.Namespace
	}
	if secretKey.Namespace != symphony.Namespace {
		return nil, fmt.Errorf("security: credential secret must be in symphony namespace %q, got %q",
			symphony.Namespace, secretKey.Namespace)
	}

	if err := c.client.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("getting overlay credentials secret: %w", err)
	}

	// Get kubeconfig data - NEVER log this
	kubeconfigKey := creds.Key
	if kubeconfigKey == "" {
		kubeconfigKey = "kubeconfig"
	}
	kubeconfigData, ok := secret.Data[kubeconfigKey]
	if !ok {
		return nil, fmt.Errorf("kubeconfig key %q not found in secret", kubeconfigKey)
	}

	// Hash credentials to detect rotation without storing them
	credHash := hashCredentials(kubeconfigData)

	// Check cache - invalidate if credentials changed or TTL expired
	if cached, ok := c.overlayClients.Load(key); ok {
		oc := cached.(*overlayClient)
		if time.Since(oc.createdAt) < c.clientCacheTTL && oc.credentialHash == credHash {
			return oc.client, nil
		}
		// Cache expired or credentials rotated
		logger.V(1).Info("invalidating cached overlay client",
			"reason", map[bool]string{true: "credential_rotation", false: "ttl_expired"}[oc.credentialHash != credHash])
	}

	// Create REST config from kubeconfig
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}

	// SECURITY: Apply rate limiting and timeouts to prevent resource exhaustion
	restConfig.Timeout = overlayClientTimeout
	restConfig.QPS = overlayClientQPS
	restConfig.Burst = overlayClientBurst

	// Set a meaningful user agent for audit logs on the overlay
	restConfig.UserAgent = "eno-overlay-sync-controller"

	// Create client
	oc, err := client.New(restConfig, client.Options{})
	if err != nil {
		return nil, fmt.Errorf("creating overlay client: %w", err)
	}

	// Cache the client with credential hash for rotation detection
	c.overlayClients.Store(key, &overlayClient{
		client:         oc,
		createdAt:      time.Now(),
		credentialHash: credHash,
	})

	// SECURITY: Don't log secret name in production, only log that client was created
	logger.V(1).Info("created overlay client")
	return oc, nil
}

// syncOverlayResource syncs a single overlay resource to an InputMirror
func (c *Controller) syncOverlayResource(
	ctx context.Context,
	symphony *apiv1.Symphony,
	overlayClient client.Client,
	ref apiv1.OverlayResourceRef,
) (time.Duration, error) {
	logger := logr.FromContextOrDiscard(ctx).WithValues("key", ref.Key, "resourceName", ref.Resource.Name)

	// SECURITY: Validate the resource kind is allowed to be synced
	gk := schema.GroupKind{Group: ref.Resource.Group, Kind: ref.Resource.Kind}
	if !c.allowedKinds[gk] {
		return 0, fmt.Errorf("security: resource kind %q is not allowed to be synced from overlay", gk.String())
	}

	// Fetch from overlay
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   ref.Resource.Group,
		Version: ref.Resource.Version,
		Kind:    ref.Resource.Kind,
	})

	objKey := types.NamespacedName{
		Name:      ref.Resource.Name,
		Namespace: ref.Resource.Namespace,
	}

	err := overlayClient.Get(ctx, objKey, obj)
	if err != nil {
		if errors.IsNotFound(err) && ref.Optional {
			logger.V(1).Info("optional overlay resource not found, skipping")
			// Update InputMirror to reflect missing state
			return c.updateMirrorMissing(ctx, symphony, ref)
		}
		return 0, fmt.Errorf("getting overlay resource: %w", err)
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

		// Serialize the resource data
		rawData, err := json.Marshal(obj.Object)
		if err != nil {
			return fmt.Errorf("marshaling resource data: %w", err)
		}
		mirror.Status.Data = &runtime.RawExtension{Raw: rawData}
		mirror.Status.LastSyncTime = &metav1.Time{Time: time.Now()}
		mirror.Status.SyncGeneration = obj.GetResourceVersion()

		// Update conditions
		setSyncedCondition(mirror, true, "SyncSuccess", "Successfully synced from overlay cluster")

		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("creating/updating InputMirror: %w", err)
	}

	logger.V(1).Info("synced overlay resource", "result", result, "mirrorName", mirrorName)

	// Determine requeue interval
	syncInterval := DefaultSyncInterval
	if ref.SyncInterval != nil {
		syncInterval = ref.SyncInterval.Duration
	}
	return syncInterval, nil
}

// updateMirrorMissing updates the InputMirror to reflect that the source resource is missing
func (c *Controller) updateMirrorMissing(
	ctx context.Context,
	symphony *apiv1.Symphony,
	ref apiv1.OverlayResourceRef,
) (time.Duration, error) {
	mirrorName := inputMirrorName(symphony.Name, ref.Key)
	mirror := &apiv1.InputMirror{}
	err := c.client.Get(ctx, types.NamespacedName{Name: mirrorName, Namespace: symphony.Namespace}, mirror)
	if errors.IsNotFound(err) {
		// No mirror exists, nothing to update
		return DefaultSyncInterval, nil
	}
	if err != nil {
		return 0, err
	}

	// Update condition to reflect missing state
	setSyncedCondition(mirror, false, "SourceNotFound", "Optional source resource not found in overlay")
	mirror.Status.Data = nil

	if err := c.client.Status().Update(ctx, mirror); err != nil {
		return 0, err
	}

	syncInterval := DefaultSyncInterval
	if ref.SyncInterval != nil {
		syncInterval = ref.SyncInterval.Duration
	}
	return syncInterval, nil
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
	for _, ref := range symphony.Spec.OverlayResourceRefs {
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
