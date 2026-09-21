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

### Automatic Deletion

Resources are automatically cleaned up when:
- They're no longer returned by your synthesizer
- Their parent composition is deleted

```yaml
# Prevent cascading deletion
metadata:
  annotations:
    eno.azure.io/deletion-strategy: orphan
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

## Sharding

The Eno reconciler watches Compositions across all namespaces by default. The following flags control its scope:
- **--composition-namespace**: Optionally limit Composition and ResourceSlice watches to the given namespace. Defaults to all namespaces, independently of `POD_NAMESPACE`.
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
## Tombstone Recovery and Inventory

`--enable-backup-operator` defaults to false. When enabled, backup requires `POD_NAMESPACE`, populated from the pod's `metadata.namespace` through the Downward API. Backup uses separate namespace-scoped watches for Compositions and ResourceSlice metadata in that namespace, respecting `--composition-label-selector` for Compositions and the existing Composition-only resource filter. It does not change the normal reconciler's `--composition-namespace` behavior. Only Compositions in the backup namespace wait for recovery; those outside it continue normal reconciliation. When backup is disabled, it registers no watches and imposes no `POD_NAMESPACE` requirement. Inventory ConfigMaps remain in downstream `kube-system`.

After synthesis, `tombstoneRecovery` reads the saved downstream inventory and persists missing tombstones only when `TombstoneRecoveryRequired` is true; otherwise the controller records a `NotNeeded` recovery decision without reading inventory. Reconstitution waits for this synthesis-bound decision. `TombstoneRecoveryRequired` is computed independently for each synthesis rather than inherited from the preceding synthesis.

Recovery uses the same group/kind/namespace/name identity resolution as normal synthesis: a Patch targeting a historical resource protects that resource from a recovery tombstone even when no full manifest is present. Patch pseudo-resources remain excluded from newly recorded inventories.

Recovery overflow ResourceSlices use the Composition name as their prefix, like normal ResourceSlices, with a deterministic suffix derived from the Composition UID, synthesis UUID, and slice contents so retries reuse the same object. Long Composition names are truncated to keep the complete name within Kubernetes' 253-character limit.

Recovery-created overflow slices carry `eno.azure.io/tombstone-recovery: "true"`. ResourceSlice cleanup temporarily retains only marked, unreferenced slices whose synthesis UUID matches the current synthesis while `TombstoneRecoveryRequired` is true and recovery is unfinished, unless the Composition is deleting. Ordinary unmarked slices retain their original cleanup behavior. Cleanup rechecks protected slices every five seconds because Composition events only enqueue referenced slices. Recovery publishes overflow references and completion together, handing protection over to the normal reference checks; after completion or supersession, unreferenced slices become eligible for cleanup. Recovery retries require an existing overflow slice to have the marker in addition to matching owner and spec.

Composition status events advance recovery; successful status writes do not explicitly requeue. Superseded work explicitly requeues, and API failures use controller-runtime error retries. Fresh Composition reads occur immediately before ResourceSlice writes. These checks reject missing Compositions, deletion, and missing or changed synthesis UUIDs. Status patches rely on atomic UID/resourceVersion/synthesis UUID preconditions without an extra Composition read; rejected patches return errors for retry.

`makeInventory` returns a `*corev1.ConfigMap` ready for the controller to pass directly to `Create`. `selectInventory` also returns a ConfigMap. `decodeInventorySnapshot` returns only the decoded `[]inventoryResource`, which the controller can pass to `missingTombstones` to calculate the diff against current ResourceSlices; there is no inventory wrapper type.

Inventory is represented as a ConfigMap in `kube-system`, named `eno-inventory-<lineageHash>-<synthesisUUID>` and labeled `eno.azure.io/inventory-lineage=<lineageHash>`. The lineage hash is the lowercase hexadecimal encoding of the first 16 bytes of SHA-256 over `<compositionNamespace>/<synthesizerName>`, excluding the Composition UID so inventory can survive recreation.

The `inventory.json` data key contains only a JSON array of resource identities (group, version, kind, namespace, and name), labels, and `eno.azure.io/readiness-group` / `eno.azure.io/deletion-group` annotations. An inventory with no resources stores `[]`. Each inventory is limited to 1 MiB of ConfigMap data without truncation or sharding.

Snapshot metadata is stored in the ConfigMap's `metadata.annotations`:

| Annotation | Value |
| --- | --- |
| `eno.azure.io/inventory-format-version` | `1` |
| `eno.azure.io/inventory-composition-namespace` | Source Composition namespace, not the ConfigMap's `kube-system` namespace |
| `eno.azure.io/inventory-synthesizer-name` | Source synthesizer name |
| `eno.azure.io/inventory-synthesis-uuid` | Source synthesis UUID |
| `eno.azure.io/inventory-synthesized` | Source synthesis timestamp in RFC 3339 format |

Composition name/UID, routing metadata, and Symphony identity are not stored. Only the annotation-based layout is supported; older JSON-envelope snapshots are rejected and must be replaced before use with this format.

Selection uses the greatest source `synthesized` timestamp, not ConfigMap creation time or UUID ordering. Invalid candidates or distinct synthesis UUIDs tied at the greatest timestamp prevent selection. Missing-tombstone calculation protects currently desired resources, existing tombstones, and Eno Patch targets; recorded inventories exclude tombstones and Eno Patch pseudo-resources. Inventory construction assumes the caller supplies an already-selected Composition with a ready current synthesis and its ResourceSlices. It converts the manifests directly without rechecking synthesis eligibility, re-evaluating resource filters, or constructing reconciliation resources; validation of stored inventories happens when they are decoded. Duplicate group/kind/namespace/name identities keep the first occurrence's version, labels, and annotations rather than selecting the definition used during apply.

Composition ownership can be evaluated through the existing `--resource-filter` CEL expression without resource output: definite `false` excludes the Composition, while resource-dependent unknowns remain eligible. Logical ownership must be disjoint between reconciler deployments; resource-only predicates cannot establish exclusive Composition ownership.

## Advanced Concepts

- [Overrides](./overrides.md)
- [Patch](./patch.md)
