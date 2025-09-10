# Resource Reconciliation

Reconciliation is the process where Eno continuously synchronizes your synthesized resources with the actual state in your Kubernetes cluster. The `eno-reconciler` process monitors for changes and automatically applies updates, handles deletions, and manages resource dependencies to keep your cluster in the desired state.

## What is Reconciliation?

When your synthesizer generates Kubernetes resources, reconciliation ensures those resources actually exist and match their intended configuration in your cluster. This includes:

- **Applying changes** when the actual state has diverged from the synthesized resources
- **Deleting resources** when they've been removed from the composition
- **Ordering operations** to respect dependencies between resources
- **Reporting status** as feedback for the composition status

Eno treats managed resources as **opaque** - it doesn't interpret their schemas or infer relationships between them.
There is only one exception: CRDs are always reconciled before CRs that use them, since CRs can't exist without their definitions.

### Update Strategy

By default, Eno uses [server-side apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/) with conflict resolution to update resources:

> 💡 **Fallback option**: The reconciler can use client-side three-way merge by setting `--disable-ssa`

**Alternative update strategies:**

```yaml
metadata:
  annotations:
    # Use full replacement instead of patches
    eno.azure.io/replace: "true"
    
    # Prevent all updates to this resource
    eno.azure.io/disable-updates: "true"

    # Prevent any mutation of this resource
    eno.azure.io/disable-reconciliation: "true"
```

### Deletion

Resources are automatically cleaned up when:
- They're no longer returned by your synthesizer
- Their parent composition is deleted

**Preserve resource after composition deletion:**

```yaml
metadata:
  annotations:
    eno.azure.io/deletion-strategy: orphan
```

**Strict deletion:**

By default, a resource is considered deleted when it no longer exists or has a non-nil `metadata.deletionTimestamp`. The `strict` strategy disables the deletion timestamp check, causing Eno to wait for all finalizers to complete before proceeding.

```yaml
metadata:
  annotations:
    eno.azure.io/deletion-strategy: strict
```

### Drift Detection and Correction

By default, resources reconcile when their expected state changes or when the reconciler restarts. For resources that may drift or need regular evaluation:

```yaml
metadata:
  annotations:
    # Re-sync every 15 minutes to correct drift
    eno.azure.io/reconcile-interval: "15m"
```

## Controlling Reconciliation Order

### Readiness Checks

Readiness checks determine when resources are considered "ready" and control the order of reconciliation operations. Eno uses [CEL (Common Expression Language)](https://github.com/google/cel-go) expressions to evaluate resource readiness.

#### Basic Readiness

```yaml
metadata:
  annotations:
    # Wait for a specific status field
    eno.azure.io/readiness: self.status.foo == 'bar'
```

#### Multiple Readiness Conditions

You can define multiple readiness checks - all must be true for the resource to be considered ready:

```yaml
metadata:
  annotations:
    # Primary readiness check
    eno.azure.io/readiness: self.status.foo == 'bar'
    
    # Additional check (both must pass)
    eno.azure.io/readiness-foo: self.status.anotherField == 'ok'
```

> 💡 **Note**: When multiple checks are used, the latest transition time determines when the resource became ready.

#### Condition-Based Readiness

For precise timing, return condition objects that include `lastTransitionTime`:

```yaml
metadata:
  annotations:
    # Use the condition's exact timestamp
    eno.azure.io/readiness-condition: |
      self.status.conditions.filter(item, 
        item.type == 'Ready' && item.status == 'True'
      )
```

> 💡 **Note**: Boolean `true` results use the current system time when readiness is first detected, while condition objects use their `lastTransitionTime` field.

### Readiness Groups

Control reconciliation order by assigning resources to numbered groups:

```yaml
metadata:
  annotations:
    eno.azure.io/readiness-group: "1"
```

#### How Groups Work

- **Default group**: Resources without `readiness-group` are in group `0`
- **Ordering**: Lower numbers reconcile first: `-2` → `-1` → `0` → `1` → `2`
- **Dependencies**: Group `N+1` waits for all group `N` resources to be ready

#### Deletion Order

By default, resources are deleted without regard to their readiness groups. To enable ordered deletion, use this annotation:

```yaml
metadata:
  annotations:
    eno.azure.io/ordered-deletion: "true"
```

With ordered deletion enabled, a resource's deletion is blocked until all resources in **higher** numbered groups have been deleted (the inverse order of reconciliation).

## Sharding

You may reconcile a subset of Compositions and/or resources by optionally passing the following flags to the Eno reconciler:
- **--composition-namespace**: Only watch compositions in the given namespace.
  ```
  --composition-namespace=default
  ```
- **--composition-label-selector**: Only watch composition that match the selector.
  ```
  --composition-label-selector=some.domain.com/type=some-type
  ```
- **--resource-filter**: Only reconcile resources that pass the given cel filter expression. Both the Composition and resource are available in the evaluation context.
  ```
  --resource-filter=composition.metadata.annotations.someAnnotation == 'some-value' && self.kind == 'ConfigMap'
  ```
  > ⚠️ Changes to composition metadata (without changing the spec) will not trigger a re-evaluation of the filter.

The flags stack up and are not mutually exclusive i.e. A resource filter will only be evaluated against resources whose Composition match the label selector, which in turn is only evaluated against Compositions in the selected namespace.
## Advanced Concepts

- [Overrides](./overrides.md)
- [Patch](./patch.md)
