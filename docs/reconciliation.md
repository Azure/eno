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

## Tombstone Recovery and Inventory

`eno-reconciler --enable-backup-operator` enables best-effort recovery of resources whose synthesis history was lost upstream but whose inventory survived downstream. The flag defaults to `false`. The operator always runs: when disabled, it acknowledges unfinished recovery for its own non-deleting Compositions with `BackupOperatorNotEnabled`, without accessing downstream inventory or changing ResourceSlices.

The operator shares the reconciler's Composition watch scope, `--resource-filter`, and downstream configuration selected by `--remote-kubeconfig` (or its existing default). Composition ownership must be disjoint between logical reconciler deployments, with routing metadata present even for empty synthesis output. Composition-level filtering partially evaluates the existing CEL expression: definite `false` excludes a Composition, while resource-dependent unknowns remain eligible. Resource-only filters cannot establish exclusive Composition ownership across deployments; use Composition routing predicates as well. The same expression filters inventoried resources and recovered tombstones.

`TombstoneRecoveryRequired` is determined independently for each synthesis, starting false rather than inheriting the preceding synthesis's flag. The executor sets it only when there is no `CurrentSynthesis` to use as history, a historical ResourceSlice reference is nil or nameless, or a referenced slice is missing. A complete historical reference list, including an empty list, leaves it false even if earlier recovery was skipped or unfinished. Other history-read errors and malformed manifest JSON continue to fail synthesis rather than silently publishing incomplete output. Consequently, an older unresolved recovery requirement does not force later syntheses with complete historical slices to compare against inventory.

Update the Composition CRD and both Eno binaries before deploying this feature. For non-deleting Compositions, reconstitution waits until `status.currentSynthesis.tombstoneRecoveryFinished.status` is true for that synthesis UUID. The decision also contains `reason`, an optional diagnostic `message`, and `synthesisUUID`. A terminal decision is not necessarily successful recovery. `NotNeeded`, `BackupOperatorNotEnabled`, `InventoryNotFound`, and `InventoryInvalid` all release the gate. Inventory-discovery and read failures instead keep recovery unfinished with `InventoryGetError` and the actual error message, retrying without an attempt limit until resolved, superseded, or the Composition starts deleting. A failed request, including an API-level 404, is not evidence that inventory is absent. ResourceSlice read, append, creation, and reference-publication failures also keep preparation unfinished and retry while the Composition remains eligible.

Inventory-read failures use the same error-retry path as other backup failures. The controller's standard workqueue rate limiter owns the in-memory backoff, with up to 20% additional jitter; there is no separate inventory retry counter or deadline. Unchanged recovery errors do not cause repeated status writes. New recovery decisions omit `inventoryAttempts`; the legacy field and its existing validation remain for compatibility, but the operator neither reads nor increments it. This retry-policy change does not require expanding the CRD schema. Existing terminal decisions, including `InventoryGetError` decisions written by older binaries, are not reopened. Synthesis scheduling and the non-inheritance rule remain unchanged, so a newer synthesis with complete history can still supersede unfinished recovery and report `NotNeeded`.

Recovery compares the selected inventory with the complete current ResourceSlices, preserving currently desired identities and existing tombstones. Missing tombstones retain saved labels and the original `eno.azure.io/readiness-group` and `eno.azure.io/deletion-group` annotations. They append to existing slices without changing existing manifest indices or resource status. Additional slices are created only when existing capacity is insufficient. Recovery never requests, cancels, or delays synthesis; superseded work is abandoned without rolling back submitted writes.

Composition deletion bypasses backup entirely for recovery and inventory recording. When `metadata.deletionTimestamp` is set, the operator discards pending work without reading inventory, modifying ResourceSlices, or writing recovery status, including disabled-mode acknowledgments. Reconstitution skips the recovery gate and lets the existing deletion flow run even if the deletion controller changed the synthesis UUID or recovery is unfinished. Authoritative eligibility checks stop in-progress backup work when deletion is observed; already-submitted requests may still finish because the checks and writes are not atomic. Deletion uses the existing ResourceSlice history rather than recovering inventory-only identities, so resources known only to inventory are not recovered on this path. Best-effort inventory retirement after the Composition disappears remains separate and does not hold deletion.

After recovery preparation is terminal and `CurrentSynthesis.Ready` is set, the operator records the complete non-tombstoned inventory in downstream `kube-system`. Each ConfigMap is named `eno-inventory-<lineageHash>-<synthesisUUID>` and labeled `eno.azure.io/inventory-lineage=<lineageHash>`. The hash is the lowercase hexadecimal encoding of the first 16 bytes of SHA-256 over `<compositionNamespace>/<synthesizerName>`; it intentionally excludes the Composition UID. ConfigMaps have no Composition owner reference, so inventory survives Composition recreation.

The `inventory.json` data key contains format version `1`: `formatVersion`, `compositionNamespace`, `synthesizerName`, `synthesisUUID`, `synthesized`, and a `resources` array. Each resource records `group`, `version`, `kind`, `namespace`, `name`, labels, and the two group annotations when present, but no readiness observations. Eno Patch pseudo-resources are excluded. The greatest source `synthesized` timestamp selects the snapshot; ConfigMap creation time and UUID ordering are not used. Any invalid candidate, or distinct synthesis UUIDs tied at the greatest timestamp, prevents selection and cleanup.

New snapshots also include `sourceComposition`, containing the source Composition's `name`, `uid`, `labels`, and `annotations`, plus `symphony: {name, uid}` when it has a controlling Symphony. The source namespace is the existing `compositionNamespace` field. This metadata supports cleanup after Composition deletion; recovery does not require the restoring Composition or Symphony UID to match it. Older version-1 snapshots without this metadata remain valid recovery inputs but are retained during automatic retirement. Retrying an already-recorded legacy snapshot compares all core inventory contents without inventing cleanup metadata. Readers that predate this optional field reject unknown JSON fields: upgrade all inventory consumers before enabling new writers, and avoid rolling back those readers while new-format snapshots remain.

New snapshots are persisted and verified before older snapshots are deleted with UID and resourceVersion preconditions. Recording and cleanup failures retry without changing resource readiness or restarting recovery. Unresolved historical inventory is retained, including after `InventoryGetError` or `InventoryInvalid`. For the synthesis that required recovery, an initial baseline after `InventoryNotFound` does not clear `TombstoneRecoveryRequired`; that synthesis's flag clears only after `FinishedTombstoneRecovery`, readiness, and confirmed replacement inventory for the same Composition UID and synthesis UUID. This flag is not inherited by the next synthesis, which independently evaluates its available history.

After observing a Composition deletion, an enabled backup operator performs best-effort inventory retirement only if the deleted Composition and snapshot's saved source metadata match its ownership scope, the owning Symphony still exists with the same UID and is not deleting, and no `spec.variations` entry has the source Composition's `spec.synthesizer.name`. Inventory is retained when `eno.azure.io/symphony-deleting=true`, the owner is absent or replaced, the variation still exists, or a Composition in the namespace uses the same name or synthesizer. The operator verifies these conditions directly against the API before each snapshot deletion and uses ConfigMap UID and resourceVersion preconditions. It validates discovered snapshots before cleanup; invalid or ambiguously ordered inventories are retained.

Retirement adds no finalizer and does not delay Composition deletion or alter resource cleanup. Deletion events retain their Composition identity in memory so failed requests can retry after the object disappears. An enabled operator also registers one manager-managed background GC loop, following leader election and shutdown, to find eligible inventory left by missed events or restarts. There is no additional controller, Deployment, or flag.

The initial GC sweep waits a randomized delay of up to one minute. Later sweeps run five minutes after the preceding sweep, with up to 20% additional jitter. Each sweep is sequential, has a 30-second deadline, and permits at most 100 HTTP API requests, including retries, and 20 ConfigMap deletion attempts. Requests have a timeout of at most 10 seconds and use the existing upstream/downstream REST configurations and rate limits. These GC limits are independent of recovery retries.

GC discovers only inventory-labeled ConfigMaps using metadata-only pages of at most 100 entries. It reads and validates each candidate payload without loading ResourceSlices or calculating tombstones. It reuses Composition and Symphony reads for candidate selection within a sweep, but deletion eligibility is always rechecked directly against the API. All discovered snapshots for a lineage are validated before deletion; missing, changed, invalid, or ambiguously ordered candidates defer that lineage. Pagination, lineage-validation progress, and up to 20 deletion candidates remain in GC-owned memory across budget-limited sweeps. Expired metadata continuation tokens are logged and resumed from the API's replacement token when available, or discovery restarts without treating the error as an empty inventory.

Event-driven and periodic cleanup share the same deletion guards. Logs distinguish them through `cleanupTrigger=compositionDeletion` and `cleanupTrigger=periodic`, with per-sweep request/deletion counts. Missing ownership metadata, uncertain reads, or absent/deleting/replaced Symphonies retain inventory. Cleanup does not guarantee that snapshots disappear before feature re-enablement; once the variation is desired again, its inventory is retained and remains available to a synthesis that independently requires recovery. Rechecking the Symphony is not atomic with downstream deletion, so an already-submitted delete can finish after a variation is re-added or teardown starts.

Before enabling backup, verify downstream permissions for `get`, `list`, `create`, and `delete` on ConfigMaps in `kube-system`, and upstream permissions for Composition reads/lists/status patches, Symphony reads, and ResourceSlice reads/creation/patches. No deployment credentials are implicitly granted permissions by enabling the flag.

**Limits:** Each inventory is one ConfigMap, limited to 1 MiB of data; oversized snapshots fail recording without truncation, compression, or sharding. Source timestamps are assumed to preserve synthesis publication order, so clock skew can invalidate that assumption. The unchanged ResourceSlice status aggregation may report `Ready` before appended tombstones have corresponding results; recording trusts that timestamp, not independent confirmation of every deletion. The unchanged cleanup controller may delete an unreferenced overflow slice before reference publication, including after its five-second grace period. Existing missing-slice handling may then independently request synthesis; backup does not prevent or initiate that request.

## Advanced Concepts

- [Overrides](./overrides.md)
- [Patch](./patch.md)
