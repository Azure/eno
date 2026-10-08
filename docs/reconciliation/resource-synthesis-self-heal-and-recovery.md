# Resource Synthesis Self-Heal and Recovery

## Purpose and Scope

- **Self-heal:** Request a new synthesis when current ResourceSlice references are unavailable, including terminating slices that would block pending recovery.
- **Recover:** Read saved inventory and restore missing tombstones into the current synthesis. A tombstone is a manifest with `Deleted: true`; normal resource reconciliation performs the downstream deletion.
- **Coordinate:** Prevent reconstitution and readiness from advancing before the current synthesis has a terminal recovery decision.
- **Record:** Save the current Ready synthesis's inventory as one or more Secrets for future recovery.
- **Not included:** General inventory garbage collection or cleanup on Composition deletion. Stable Secret names replace prior contents without deleting leftover chunks.
- **Not guaranteed:** Recovery of every historical resource when inventory is missing or invalid, or atomic behavior across Composition status, ResourceSlices, and downstream resources.
- **Related guide:** [Resource Reconciliation](./reconciliation.md) explains ordinary apply, deletion, readiness, and sharding behavior.

## Enablement and Ownership

- `--enable-tombstone-recovery` enables the operator; it defaults to `false`.
- When enabled, `POD_NAMESPACE` must contain the reconciler pod's namespace, supplied through the Downward API.
- The recovery controller uses a separate informer cache scoped to `POD_NAMESPACE`; its Composition watch also respects `--composition-label-selector`.
- With recovery enabled, startup requires `--composition-namespace` to match `POD_NAMESPACE`; missing or mismatched namespaces are rejected immediately before recovery controller registration and before the manager starts. Recovery configuration is validated separately from the generic reconciliation constructor. The recovery controller is registered only when enabled, using the same downstream configuration as reconciliation.
- Each Composition must opt in with the annotation below. Missing annotations, labels with the same key, and annotation values other than the exact string `"true"` do not opt in.
- Recovery eligibility does not evaluate `--resource-filter`. Normal resource reconciliation still evaluates that filter against actual resources, including recovered tombstones.
- When disabled, the recovery controller registers no watches and imposes no `POD_NAMESPACE` requirement. Reconstitution can still acknowledge disabled recovery for annotated Compositions.
- Inventory recording requires downstream permission to get, list, create, and update Secrets in `kube-system`. Recovery reads Secrets only; the unreleased ConfigMap inventory format is not supported.

```yaml
metadata:
  annotations:
    eno.azure.io/recovery-enabled: "true"
```

### Deployment Requirements

- Configure a single recovery owner for each Composition; the annotation does not enforce exclusive ownership between reconciler deployments.
- When adding the recovery annotation to an existing Composition, trigger a new synthesis. The annotation alone does not force synthesis in Eno.
- ResourceSlices are Eno-owned; external users and controllers are not expected to delete or modify them.

## Status: Three Different Questions

- **`eno.azure.io/recovery-enabled`:** Does this Composition opt into recovery?
- **`TombstoneRecoveryRequired`:** Does this synthesis have potentially missing history that still requires recovery?
- **`TombstoneRecoveryStatus`:** What recovery decision has been recorded for this synthesis?
- **`IsTombstoneRecoveryFinished()`:** Is the decision finished (`Status: true`) and bound to the current synthesis UUID, including skipped recovery?
- A first synthesis requires recovery because resources from an earlier Composition incarnation may remain downstream.
- A successor inherits an outstanding recovery requirement. Complete slice references do not erase work that recovery has not yet performed.
- `TombstoneRecoveryOperatorNotEnabled` releases the gates but preserves an existing requirement for the next synthesis. It means "skipped for now," not "historical resources recovered."
- Other terminal decisions do not carry the recovery requirement forward. Unreconciled tombstones already present in slices are carried forward by normal synthesis.
- A terminal decision is not proof of downstream deletion or resource readiness.

## Normal Flow

```text
Synthesis detects incomplete history or inherits an outstanding requirement
    -> Recovery checks annotation and current synthesis
    -> Read inventory and all current ResourceSlices
    -> Calculate missing tombstones
    -> Plan placement and persist tombstones
    -> Publish overflow references and a terminal recovery decision together
    -> Reconstitution loads resources
    -> Normal reconciliation applies resources and processes deletions
    -> ResourceSlice aggregation reports Reconciled and Ready
    -> Record the Ready synthesis's inventory in downstream Secrets
```

- **No recovery requirement:** Record `NotNeeded` without reading inventory.
- **No inventory:** Record `InventoryNotFound` and allow normal reconciliation; historical cleanup cannot be reconstructed from an absent snapshot.
- **Inventory matches current identities:** Write no additional tombstones and record `FinishedTombstoneRecovery`.
- **Historical identities are missing:** Persist tombstones for those identities and record `FinishedTombstoneRecovery`.
- **Operator disabled:** Reconstitution records `TombstoneRecoveryOperatorNotEnabled` for an annotated, synthesized, non-deleting Composition without a terminal decision, then proceeds.
- **Unannotated or deleting Composition:** Bypass recovery gates and retain normal reconciliation/deletion behavior.

## Component Responsibilities

### Synthesis Executor

- **Does:** Read available previous slices, detect missing or nameless references, and publish the next synthesis's recovery requirement.
- **Does:** Preserve outstanding requirements across unfinished or disabled recovery; carry forward unreconciled tombstones not replaced by desired output.
- **Does not:** Read saved inventory, create a recovery decision, or directly delete downstream resources.
- **Entry point:** [`fetchCurrentSynthesisResSlices`](../../internal/execution/executor.go).

### Tombstone Recovery Controller

- **Does:** Select opted-in Compositions, read inventory when required, load complete current slices, and calculate missing resource identities.
- **Does:** Treat current manifests, existing tombstones, and Eno Patch targets as present, using group/kind/namespace/name identity.
- **Does:** Record retriable errors or terminal decisions, publishing overflow references together with completion.
- **Does:** On a subsequent event, record inventory when the persisted current synthesis is Ready and its recovery decision is finished.
- **Does not:** Change an existing desired manifest into a tombstone, directly delete downstream resources, or garbage-collect inventory Secrets.
- **Entry points:** [`Reconcile` and `recoverMissingTombstones`](../../internal/controllers/tombstonerecovery/controller.go), [`missingTombstones`](../../internal/controllers/tombstonerecovery/inventory.go).

### Packing Planner and Writer

- **Planner does:** Calculate first-fit additions to existing slices and batches for new overflow slices, without mutating inputs or calling Kubernetes APIs.
- **Writer does:** Execute the plan with optimistic locking, deterministic overflow names, collision validation, and freshness checks immediately before each slice write.
- **Does not:** Publish Composition completion; that happens after the writer succeeds.
- **Entry points:** [`planTombstones` and `writeTombstones`](../../internal/controllers/tombstonerecovery/slices.go).

### Inventory Recording

- **Does:** Read every referenced current ResourceSlice before preparing any Secret writes; missing, terminating, malformed, or unreadable slices stop the attempt.
- **Does:** Exclude tombstones and Patch resources, sort inventory identities, and deterministically pack complete JSON arrays into chunks within the Secret data limit.
- **Does:** Recheck the current synthesis UUID, opt-in, deletion state, readiness, and finished recovery decision before writes. Namespace and label-selector scope are enforced by the informer cache that enqueues the Composition.
- **Does:** Reuse stable Secret names, skip identical writes, and use resourceVersion-protected updates. Conflicts and transient failures retry through the controller.
- **Does not:** Reopen a finished recovery decision, rewrite different contents under the same synthesis UUID, or remove leftover Secrets.
- **Entry points:** [`recordInventory` and `writeInventoryChunk`](../../internal/controllers/tombstonerecovery/recording.go).

### Reconstitution

- **Does:** Wait for the current synthesis's terminal recovery decision when recovery is enabled and the Composition is opted in and not deleting.
- **Does:** Record the disabled decision using optimistic locking when the operator is off.
- **Does not:** Perform inventory recovery or decide when downstream resources are Ready.
- **Entry point:** [`reconstitutionSource.Reconcile`](../../internal/controllers/reconciliation/reconstitution.go).

### ResourceSlice Lifecycle and Status Writing

- **Does:** Withhold `Reconciled` and `Ready` for opted-in, non-deleting Compositions while their recovery decision is unfinished.
- **Does:** Treat a resource-status array shorter than the manifest array as unfinished; the status writer extends the array while preserving existing entries.
- **Does:** Request resynthesis for missing/nameless current references and for terminating current slices during pending recovery.
- **Does not:** Override `eno.azure.io/ignore-side-effects: "true"`, an existing in-flight synthesis, or an already-requested resynthesis.
- **Does not:** Mark resources Ready merely because the recovery decision is terminal.
- **Entry points:** [`sliceController.Reconcile`](../../internal/controllers/resourceslice/slice.go), [`ResourceSliceWriteBuffer.buildPatch`](../../internal/flowcontrol/writebuffer.go).

### ResourceSlice Cleanup

- **Does:** Retain referenced slices and temporarily protect marked, unpublished overflow for the current synthesis while opted-in recovery is required and unfinished.
- **Does:** Recheck protected overflow every five seconds, since Composition events do not enqueue unreferenced slices.
- **Does:** Hand protection over to normal reference checks after publication. Unreferenced overflow becomes eligible for cleanup after completion, supersession, opt-out, or Composition deletion.
- **Does not:** Reconcile downstream resources or protect ordinary unmarked abandoned slices merely because recovery is pending.
- **Entry point:** [`cleanupController`](../../internal/controllers/resourceslice/slicecleanup.go).

## Edge Cases and Failure Handling

### A New Synthesis Starts During Recovery

- **Situation:** S1 begins while the current synthesis S0 is still recovering.
- **Handling:** S0 remains current until S1 is published. S1 inherits unfinished recovery when it captures history; if recovery already completed, available unreconciled tombstones carry forward through normal slicing.
- **Boundary:** Starting S1 does not itself cancel S0 recovery. If S1 fails before publication, S0 remains current.

### A New Synthesis Becomes Current During Recovery

- **Situation:** Recovery work was calculated for S0, but S1 becomes current.
- **Handling:** Fresh Composition reads before writes detect supersession. Completion patches test the Composition UID, resourceVersion, and current UUID, preventing S0 from publishing completion or attaching references to S1.
- **Boundary:** The freshness read and slice write are not a transaction; an obsolete slice write can still occur between them. Continuous supersession can delay recovery.

### Inventory Read Fails or Inventory Is Unusable

- **Situation:** The inventory API fails, no inventory exists, or selection/decoding classifies inventory as invalid.
- **Handling:** API failures record an unfinished error and retry. Missing inventory records terminal `InventoryNotFound`; invalid inventory records terminal `InventoryInvalid`.
- **Boundary:** Missing/invalid inventory deliberately releases the gate without guaranteeing historical cleanup. Permanent API failures can leave recovery blocked; retrying does not repair permissions or connectivity.

### Current ResourceSlices Are Missing, Terminating, or Malformed

- **Situation:** Recovery cannot build a complete current-resource view.
- **Handling:** Recovery rejects incomplete input rather than treating it as resource absence. The lifecycle controller requests resynthesis for missing/nameless references, and for terminating current slices while recovery is pending; it checks later slices even if an earlier slice has not received status.
- **Boundary:** Existing resynthesis guards still apply. Malformed manifest data or persistent read errors are reported and retried, not automatically repaired.

### Only Some Tombstones Are Persisted

- **Situation:** An append/create succeeds, but a later write or completion publication fails.
- **Handling:** Retry reloads current references and recomputes missing identities. Existing tombstones are not duplicated. Deterministic overflow names allow reuse after checking owner, marker, spec, and deletion state.
- **Boundary:** This is idempotent retry, not rollback. Persisted writes remain; conflicting or terminating overflow objects produce errors rather than being overwritten.

### Tombstones Exceed Available Slice Capacity

- **Situation:** Existing slices cannot hold all recovered manifests.
- **Handling:** Fill existing slices first, then split leftovers into new slices within `resource.MaxSliceJSONBytes` (512 KiB of manifest strings). Reject any individual oversized tombstone before writing the batch.
- **Boundary:** The limit measures manifest strings, not the entire serialized ResourceSlice object.

### Empty Synthesis or Incomplete Status Arrays

- **Situation:** An empty synthesis appears ready before inventory recovery, or old status entries do not cover appended tombstones.
- **Handling:** Recovery completion gates both `Ready` and `Reconciled`, including empty syntheses. Normal aggregation then waits for all resource-status entries.
- **Boundary:** Terminal recovery decisions release the gate but do not replace normal reconciliation/readiness requirements.

### Concurrent Status Writes

- **Situation:** The synthesis, Composition metadata, recovery decision, or slice status changes before a pending write.
- **Handling:** Recovery publication uses atomic JSON Patch preconditions; disabled acknowledgment and slice appends use optimistic locking. Status-array extension checks the prior array before replacing it.
- **Boundary:** Conflicting operations retry; these checks do not create a transaction across multiple objects.

### Recovery Is Disabled, Then Enabled Later

- **Situation:** A synthesis needs recovery but the operator is disabled.
- **Handling:** Reconstitution records terminal `TombstoneRecoveryOperatorNotEnabled`. A successor retains the requirement and starts with its own recovery decision, allowing recovery when enabled.
- **Boundary:** Enabling the operator does not reopen an already-terminal decision on the same synthesis. A new synthesis is needed.

### Composition Deletion, Opt-Out, or Reconciler Restart

- **Situation:** The Composition begins deleting, loses its opt-in annotation, or the reconciler restarts.
- **Handling:** Deleting/unannotated Compositions bypass recovery gates. Pre-write checks reject observed deletion or opt-out. A restarted controller reconstructs progress from persisted slices and status.
- **Boundary:** Removing the annotation does not roll back tombstones already written; recovery-specific cleanup protection ends for unreferenced overflow.

### Inventory Recording Fails or Is Superseded

- **Situation:** A Secret write fails, a writer conflicts, or the current synthesis changes during a multi-chunk upload.
- **Handling:** Report and retry errors; abandon superseded work and reevaluate the current synthesis. Reuse stable names on retries and refuse to overwrite an observed newer snapshot with an older one.
- **Boundary:** Freshness checks are not a cross-cluster transaction. Partial replacement can leave mixed chunks; recovery records `InventoryInvalid` and skips that snapshot rather than guessing or falling back to older history.

## Inventory Format and Helper Contract

- **Location and type:** Opaque Secrets in downstream `kube-system`, independent of the namespace containing the Composition and reconciler.
- **Name:** `<comp-name>-<lineageHash>-<chunk-index>`, with the Composition prefix truncated if needed to fit the 253-character name limit. Names are reused across syntheses.
- **Lineage:** Hash of Composition namespace and synthesizer name, stored in both the `eno.azure.io/inventory-lineage` annotation and lookup label. Composition name/UID is not part of this identity, allowing recovery across recreation.
- **Payload:** `data["inventory.json"]` contains a JSON array of group/version/kind/namespace/name identities, labels, and the `eno.azure.io/readiness-group` and `eno.azure.io/deletion-group` annotations. Empty inventory is one Secret containing `[]`.
- **Source metadata:** `eno.azure.io/inventory-format-version` is `1`; source annotations are `eno.azure.io/inventory-composition-name`, `eno.azure.io/inventory-composition-namespace`, `eno.azure.io/inventory-synthesizer-name`, `eno.azure.io/inventory-synthesis-uuid`, and `eno.azure.io/inventory-synthesized`.
- **Source name validation:** The source Composition name annotation validates the stable Secret name. It need not equal the current Composition name, and no source Composition UID match is required.
- **Chunk metadata:** `eno.azure.io/inventory-chunk-index` is zero-based; `eno.azure.io/inventory-chunk-count` gives the expected number of chunks. Every chunk has the same source UUID, timestamp, and count.
- **Selection:** Choose the greatest source `Synthesized` timestamp in RFC 3339 format, not Secret creation time or UUID ordering. Different source UUIDs tied for newest are invalid. Do not fall back to older inventory.
- **Completeness:** Require exactly one chunk for every expected index, consistent snapshot metadata, valid resource identities, and decodable JSON arrays. Missing/mixed/invalid chunks, invalid timestamps, or ambiguous selection produce `InventoryInvalid`; an empty Secret list produces `InventoryNotFound`.
- **Construction:** `makeInventory` returns Secret chunks after reading a complete current-slice view. It excludes tombstones and Patch pseudo-resources, keeps the first duplicate identity's version/metadata, and sorts entries before packing.
- **Size:** Each chunk stays within the 1 MiB decoded Secret-data limit, including JSON brackets and commas. Entries are never split; an entry that cannot fit causes an explicit error before writes.
- **Replacement:** A new snapshot with fewer chunks ignores old extra chunks by selecting the new source UUID and declared count. Leftover Secrets are not deleted.
- **No resource re-selection:** The inventory builder does not re-evaluate resource filters. Stored inventory checks and decoding are separate from construction.
- **Not a full backup:** Inventory contains deletion identity/metadata, not full resource specifications or Symphony identity.

## Known Limitations and Follow-ups

- **Late opt-in without resynthesis:** Recovery can change slice membership under an already-cached UUID. The cache does not automatically load those added manifests. Trigger a new synthesis after enabling recovery rather than relying on this unsupported transition.
- **Pre-existing executor slice UUID assignment:** This work does not change how executor slices receive their synthesis UUID. Resynthesis for terminating current slices mitigates the resulting recovery/finalizer wait cycle; it does not eliminate the underlying cleanup race.
- **Concurrent downstream deletion:** An already-running deletion can overlap a newer synthesis that wants the resource. Recovery status preconditions do not make downstream operations transactional.
- **Progress depends on the environment:** Permanent errors, inhibited resynthesis, or continuous synthesis churn can prevent completion.
- **Namespace and ownership configuration:** Matching reconciliation/recovery scopes and assigning a single recovery owner per Composition are deployment responsibilities, not guarantees enforced by the annotation.
- **Follow-up implementation:** Inventory garbage collection and Composition-deletion handling.
- **Follow-up hardening:** Admission policies restricting unauthorized inventory or ResourceSlice changes, while permitting legitimate cleanup and garbage collection.
- **Follow-up validation:** End-to-end coverage is deferred; focused unit regressions exercise recovery decisions, inheritance, partial-write retries, status races, and lifecycle guards.
