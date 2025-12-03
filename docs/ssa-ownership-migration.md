# SSA Ownership Migration Design

## Problem Statement

When migrating resources from previous field managers (e.g., `Go-http-client`, `kube-controller-manager`) to `eno`, we encounter split ownership scenarios where different managers own different fields within the same logical unit (e.g., a container).

### Example of Split Ownership

```yaml
# managedFields showing split ownership
- manager: eno
  fieldsV1:
    f:spec:
      f:template:
        f:spec:
          f:initContainers:
            k:{"name":"base-os-bash"}:
              f:image: {}

- manager: Go-http-client
  fieldsV1:
    f:spec:
      f:template:
        f:spec:
          f:initContainers:
            .: {}
            k:{"name":"base-os-bash"}:
              .: {}
              f:command: {}
              f:imagePullPolicy: {}
              f:name: {}
              f:resources: {}
              f:securityContext: {}
```

### The Issue

**When attempting to remove an initContainer** (e.g., `base-os-bash`), eno performs a dry-run to validate the change. This is when the error occurs:

```
spec.template.spec.initContainers[0].image: Required value
```

**Root Cause:**

When eno sends the updated desired state (without `base-os-bash`), Kubernetes SSA performs a three-way merge:

1. **Live state**: Has `base-os-bash` with all fields
2. **Eno's desired state**: No `base-os-bash` (wants to remove it)
3. **Ownership rules**: Split ownership where:
   - `eno` owns `spec.template.spec.initContainers[name=base-os-bash].image`
   - `Go-http-client` owns other fields like `command`, `resources`, `securityContext`

During the merge, SSA sees:
- `eno` (as field manager) is removing its owned field `image`
- `Go-http-client` still "owns" the other fields, so SSA keeps them
- Result: A partial container with `command`, `resources`, but **no `image`**
- Validation fails because `image` is a required field

This happens specifically during **removal operations** where split ownership causes the merged object to violate schema requirements. The same issue can occur when modifying containers, but removal is the most common trigger.

## Design Goals

1. **Atomic Ownership for InitContainers**: Ensure `eno` owns all fields of initContainers before removing them
2. **Idempotent Migration**: Detect when migration is already complete and skip redundant operations
3. **Focused Scope**: Migrate only `spec.template.spec.initContainers` field for now
4. **No Dry-Run Failures**: Eliminate "Required value" errors during initContainer removal
5. **Standalone Implementation**: Independent from existing `fieldmanager.go` logic
6. **Quick Fix**: Minimal implementation to unblock initContainer removal operations

## Solution Design

### High-Level Approach

Before performing any update operation that might trigger dry-run validation:

1. **Check Current Ownership**: Analyze `managedFields` to determine if `eno` fully owns the target scope
2. **Migrate if Needed**: If split ownership detected, perform a one-time SSA apply with `force=true` to transfer all ownership to `eno`
3. **Proceed with Update**: Once `eno` has full ownership, proceed with the actual update/dry-run

### Ownership Scope

**For this initial implementation, we target only initContainers:**
- **Deployment**: `spec.template.spec.initContainers`
- **StatefulSet**: `spec.template.spec.initContainers`
- **DaemonSet**: `spec.template.spec.initContainers`
- **Job**: `spec.template.spec.initContainers`

This narrower scope:
- Solves the immediate initContainer removal problem
- Minimizes risk by not touching containers, volumes, or other fields
- Can be expanded later to broader pod template scope

**Why initContainers only:**
- This is where the current error occurs
- InitContainers are typically infrastructure setup, not application code
- Lower risk than migrating main containers
- Easier to validate and rollback if needed

### Implementation Components

#### 1. Ownership Detection

```go
// OwnershipStatus represents the ownership state of a resource scope
type OwnershipStatus struct {
    FullyOwnedByEno bool
    OtherManagers   []string  // List of other managers with fields in scope
    ScopeExists     bool      // Whether the scope exists in the resource
}

// CheckOwnership analyzes managedFields to determine ownership status
// for the given scope (e.g., "spec.template.spec.initContainers")
func CheckOwnership(resource *unstructured.Unstructured, scope string, enoManager string) (*OwnershipStatus, error) {
    status := &OwnershipStatus{
        FullyOwnedByEno: false,
        OtherManagers:   []string{},
        ScopeExists:     false,
    }
    
    // Check if scope exists in actual resource
    scopePath := strings.Split(scope, ".")
    _, found, err := unstructured.NestedFieldCopy(resource.Object, scopePath...)
    if err != nil {
        return nil, err
    }
    if !found {
        return status, nil
    }
    status.ScopeExists = true
    
    // Parse managedFields
    managedFields := resource.GetManagedFields()
    if len(managedFields) == 0 {
        // No managed fields = legacy resource, needs migration
        return status, nil
    }
    
    hasEnoFields := false
    scopePrefix := scope // e.g., "spec.template.spec.initContainers"
    
    for _, entry := range managedFields {
        if entry.FieldsV1 == nil {
            continue
        }
        
        // Parse fieldsV1 JSON
        fieldsV1 := map[string]interface{}{}
        if err := json.Unmarshal(entry.FieldsV1.Raw, &fieldsV1); err != nil {
            continue
        }
        
        // Check if this manager has fields under our scope
        hasFieldsInScope := checkFieldsUnderScope(fieldsV1, scopePrefix)
        
        if !hasFieldsInScope {
            continue
        }
        
        if entry.Manager == enoManager {
            hasEnoFields = true
        } else {
            status.OtherManagers = append(status.OtherManagers, entry.Manager)
        }
    }
    
    // Fully owned by eno if:
    // 1. Eno has fields in scope
    // 2. No other manager has fields in scope
    status.FullyOwnedByEno = hasEnoFields && len(status.OtherManagers) == 0
    
    return status, nil
}

// checkFieldsUnderScope recursively checks if fieldsV1 contains any fields
// under the given scope prefix (e.g., "spec.template.spec.initContainers")
func checkFieldsUnderScope(fields map[string]interface{}, scopePrefix string) bool {
    // Parse scope prefix into path components
    // e.g., "spec.template.spec.initContainers" -> ["spec", "template", "spec", "initContainers"]
    scopeParts := strings.Split(scopePrefix, ".")
    
    // Navigate through fieldsV1 structure
    current := fields
    for _, part := range scopeParts {
        fieldKey := "f:" + part
        next, ok := current[fieldKey]
        if !ok {
            return false
        }
        
        current, ok = next.(map[string]interface{})
        if !ok {
            return false
        }
    }
    
    // If we got here, fields exist under this scope
    return true
}
```

#### 2. Ownership Migration

```go
// MigrateOwnership transfers ownership of the specified scope to eno
// using Server-Side Apply with force=true
func (c *Controller) MigrateOwnership(
    ctx context.Context,
    resource *unstructured.Unstructured,
    scope string,
) (bool, error) {
    logger := logr.FromContextOrDiscard(ctx).WithValues(
        "resource", resource.GetName(),
        "namespace", resource.GetNamespace(),
        "kind", resource.GetKind(),
        "scope", scope,
    )
    
    // Step 1: Check current ownership status
    status, err := CheckOwnership(resource, scope, "eno")
    if err != nil {
        return false, fmt.Errorf("checking ownership: %w", err)
    }
    
    // Step 2: Skip if already fully owned
    if status.FullyOwnedByEno {
        logger.V(1).Info("ownership already migrated, skipping")
        return false, nil
    }
    
    if !status.ScopeExists {
        logger.V(1).Info("scope does not exist in resource, skipping migration")
        return false, nil
    }
    
    logger.V(0).Info("migrating ownership to eno",
        "otherManagers", status.OtherManagers)
    
    // Step 3: Extract the scope content from live resource
    scopePath := strings.Split(scope, ".")
    scopeContent, found, err := unstructured.NestedFieldCopy(resource.Object, scopePath...)
    if err != nil {
        return false, fmt.Errorf("extracting scope content: %w", err)
    }
    if !found {
        return false, fmt.Errorf("scope not found in resource")
    }
    
    // Step 4: Build apply configuration containing only the scope
    applyConfig := &unstructured.Unstructured{}
    applyConfig.SetGroupVersionKind(resource.GroupVersionKind())
    applyConfig.SetName(resource.GetName())
    applyConfig.SetNamespace(resource.GetNamespace())
    
    // Set the scope content
    if err := unstructured.SetNestedField(applyConfig.Object, scopeContent, scopePath...); err != nil {
        return false, fmt.Errorf("building apply config: %w", err)
    }
    
    // Step 5: Perform SSA apply with force to take ownership
    opts := []client.PatchOption{
        client.ForceOwnership,
        client.FieldOwner("eno"),
    }
    
    patch := client.Apply
    if err := c.upstreamClient.Patch(ctx, applyConfig, patch, opts...); err != nil {
        return false, fmt.Errorf("applying ownership migration: %w", err)
    }
    
    logger.V(0).Info("successfully migrated ownership to eno")
    return true, nil
}
```

#### 3. Integration into Reconciliation Flow

```go
// In reconcileSnapshot function, before calling update()
func (c *Controller) reconcileSnapshot(ctx context.Context, comp *apiv1.Composition, resource *resource.Resource, snap *resource.Snapshot) error {
    logger := logr.FromContextOrDiscard(ctx)
    
    // ... existing code to get current state ...
    
    current, err := c.getCurrentState(ctx, snap)
    if err != nil {
        return err
    }
    
    // NEW: Migrate ownership if resource exists and has initContainers
    if current != nil && shouldMigrateOwnership(snap) {
        scope := getInitContainersScope(snap.GVK)
        if scope != "" {
            migrated, err := c.MigrateOwnership(ctx, current, scope)
            if err != nil {
                logger.Error(err, "failed to migrate ownership")
                // Decide: fail-fast or continue?
                // For safety, fail-fast:
                return fmt.Errorf("ownership migration failed: %w", err)
            }
            
            if migrated {
                // Re-fetch current state to get updated managedFields
                current, err = c.getCurrentState(ctx, snap)
                if err != nil {
                    return fmt.Errorf("re-fetching after migration: %w", err)
                }
            }
        }
    }
    
    // Continue with normal update flow
    updated, err := c.update(ctx, comp, resource, snap, current, false)
    // ... rest of reconciliation ...
}

// shouldMigrateOwnership determines if a resource type needs ownership migration
func shouldMigrateOwnership(snap *resource.Snapshot) bool {
    // Only migrate for workload resources with initContainers
    gk := snap.GVK.GroupKind()
    
    switch {
    case gk.Group == "apps" && gk.Kind == "Deployment":
        return true
    case gk.Group == "apps" && gk.Kind == "StatefulSet":
        return true
    case gk.Group == "apps" && gk.Kind == "DaemonSet":
        return true
    case gk.Group == "batch" && gk.Kind == "Job":
        return true
    default:
        return false
    }
}

// getInitContainersScope returns the JSONPath to initContainers for given GVK
func getInitContainersScope(gvk schema.GroupVersionKind) string {
    gk := gvk.GroupKind()
    
    switch {
    case gk.Group == "apps" && (gk.Kind == "Deployment" || gk.Kind == "StatefulSet" || gk.Kind == "DaemonSet"):
        return "spec.template.spec.initContainers"
    case gk.Group == "batch" && gk.Kind == "Job":
        return "spec.template.spec.initContainers"
    default:
        return ""
    }
}
```

## Migration Flow Diagram

```
┌─────────────────────────────────────────────────────────────┐
│ reconcileSnapshot()                                         │
└─────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│ Get current state from cluster                              │
└─────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────┐
│ Is resource a workload with pod template?                   │
└─────────────────────────────────────────────────────────────┘
                          │
                    Yes   │   No
              ┌───────────┴───────────┐
              ▼                       ▼
┌──────────────────────────┐   ┌─────────────────┐
│ CheckOwnership()         │   │ Skip migration  │
│  - Parse managedFields   │   └─────────────────┘
│  - Check if eno owns all │            │
│    fields in scope       │            │
└──────────────────────────┘            │
              │                         │
    Fully owned? No                     │
              │                         │
              ▼                         │
┌──────────────────────────┐            │
│ MigrateOwnership()       │            │
│  - Extract scope content │            │
│  - Build apply config    │            │
│  - SSA with force=true   │            │
│  - Re-fetch current      │            │
└──────────────────────────┘            │
              │                         │
              └─────────┬───────────────┘
                        ▼
              ┌───────────────────┐
              │ update()          │
              │  - Dry-run        │
              │  - Apply changes  │
              └───────────────────┘
```

## Edge Cases and Considerations

### 1. Resource Doesn't Exist Yet (current == nil)
**Behavior**: Skip migration
**Rationale**: No managedFields to migrate; first apply will establish eno ownership

### 2. Resource Has No managedFields
**Behavior**: Perform migration
**Rationale**: Legacy resource created before SSA; migration establishes proper ownership

### 3. Multiple Managers Own Different Subfields
**Example**: 
- `Go-http-client` owns `spec.template.spec.containers[0].image`
- `kubectl` owns `spec.template.spec.containers[0].resources`

**Behavior**: Migration takes ownership of entire scope from all managers
**Result**: `eno` becomes sole owner of `spec.template.*`

### 4. Migration Fails
**Behavior**: Return error, halt reconciliation for this resource
**Rationale**: Cannot safely proceed with split ownership; retry on next reconciliation

### 5. Concurrent Updates During Migration
**Scenario**: Another controller updates the resource between ownership check and migration
**Mitigation**: 
- SSA handles conflicts via resourceVersion checks
- Migration will fail with conflict error
- Next reconciliation will retry

### 6. InitContainers Field Doesn't Exist
**Behavior**: Skip migration
**Rationale**: If `spec.template.spec.initContainers` doesn't exist, there's nothing to migrate
**Detection**: `CheckOwnership` will return `ScopeExists: false`

### 7. Removing InitContainers (Primary Use Case)
**Scenario**: Eno wants to remove `base-os-bash` initContainer from ResourceSlice
**Without Migration**: 
- Eno sends updated spec without `base-os-bash`
- SSA removes only eno-owned fields (`image`)
- Other managers' fields (`command`, `resources`) remain
- Result: Incomplete container → "image: Required value" error

**With Migration**:
- Before removal, eno takes full ownership of `spec.template.spec.initContainers`
- Eno sends updated spec without `base-os-bash`
- SSA removes the entire container since eno owns all initContainers fields
- Result: Clean removal, no validation errors

**Behavior**: This is the exact problem this design solves

## Testing Strategy

### Unit Tests

1. **CheckOwnership Tests**
   - Resource with no managedFields → needs migration
   - Resource fully owned by eno → no migration needed
   - Resource with split ownership → needs migration
   - Resource with scope not present → skip migration

2. **MigrateOwnership Tests**
   - Successful migration transfers all fields to eno
   - Migration is idempotent (no-op on second call)
   - Migration fails gracefully with invalid scope
   - Migration preserves resource content

### Integration Tests

1. **Migration Flow**
   - Create resource with `kubectl` (non-eno manager)
   - Create ResourceSlice for same resource
   - Verify eno migrates ownership before first update
   - Verify subsequent updates use eno ownership

2. **Dry-Run After Migration**
   - Migrate ownership
   - Perform dry-run to remove initContainer
   - Verify no "Required value" errors

3. **Multiple Concurrent Reconciliations**
   - Trigger multiple reconciliations simultaneously
   - Verify migration happens exactly once
   - Verify no conflicts or races

## Rollout Plan

### Phase 1: Implement and Test (Week 1)
- Implement ownership detection and migration functions
- Add unit tests
- Add integration tests

### Phase 2: Canary Deployment (Week 2)
- Deploy to canary environment
- Monitor for migration errors
- Validate no regression in normal reconciliation

### Phase 3: Staged Rollout (Week 3-4)
- Roll out to 10% of clusters
- Monitor metrics: migration success rate, reconciliation latency
- Roll out to 50%, then 100%

### Phase 4: Metrics and Observability (Ongoing)
- Add metrics:
  - `eno_ownership_migrations_total{status="success|failure"}`
  - `eno_ownership_migration_duration_seconds`
  - `eno_split_ownership_detected_total`
- Add alerts for high migration failure rates

## Metrics

```go
var (
    ownershipMigrationsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "eno_ownership_migrations_total",
            Help: "Total number of ownership migrations attempted",
        },
        []string{"status"}, // "success", "failure", "skipped"
    )
    
    ownershipMigrationDuration = prometheus.NewHistogram(
        prometheus.HistogramOpts{
            Name: "eno_ownership_migration_duration_seconds",
            Help: "Time taken to perform ownership migration",
            Buckets: prometheus.DefBuckets,
        },
    )
    
    splitOwnershipDetected = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "eno_split_ownership_detected_total",
            Help: "Number of resources with split ownership detected",
        },
        []string{"kind", "other_manager"},
    )
)
```

## Future Enhancements

### Phase 2: Expand Scope Beyond InitContainers
Once initContainer migration is stable and validated:

1. **Full Pod Template Migration**: Expand to migrate entire `spec.template` to handle:
   - Main containers (not just init)
   - Volumes
   - SecurityContext
   - Other pod spec fields
   - **Rationale**: Same split ownership issues can occur with containers, but lower priority

2. **CronJob Support**: Add `spec.jobTemplate.spec.template.spec.initContainers` path
   - **Rationale**: CronJobs have nested template structure

3. **Per-Container Granularity**: Migrate individual containers instead of entire list
   - **Rationale**: Even more surgical, but more complex to implement

### Phase 3: Advanced Features

4. **Configurable Scope via Annotation**: Allow ResourceSlice to specify custom migration scope
   - Example: `eno.azure.io/migrate-ownership: "spec.template.spec.containers"`
   - **Rationale**: Flexibility for special cases

5. **Migration Status Reporting**: Add status condition to Composition
   - Track which resources have been migrated
   - Report migration failures clearly
   - **Rationale**: Observability for ops teams

6. **Automatic Rollback**: Revert ownership on failure
   - Restore previous field managers if migration causes issues
   - **Rationale**: Safety mechanism for production

7. **Selective Migration by Name**: Migrate only specific initContainers matching pattern
   - Example: Only migrate initContainers with names starting with `aks-`
   - **Rationale**: Gradual migration for complex workloads

### Why Not Now?
These enhancements are deferred because:
- InitContainer removal is the immediate blocker
- Simpler scope = lower risk for initial rollout
- Easier to validate and monitor
- Can gather telemetry before expanding scope

## References

- [Kubernetes Server-Side Apply Documentation](https://kubernetes.io/docs/reference/using-api/server-side-apply/)
- [Field Management](https://kubernetes.io/docs/reference/using-api/server-side-apply/#field-management)
- [Conflicts](https://kubernetes.io/docs/reference/using-api/server-side-apply/#conflicts)
