# Reconciliation

Synthesized compositions are reconciled into real k8s resources by the `eno-reconciler` process.

## Opacity

Eno is designed to treat the resources it manages as completely opaque - it doesn't "understand" their schema, infer dependencies, etc..

There is one exception to this rule: CRDs are always reconciled before CRs of the kind they define.

## Updates

By default, Eno uses [server-side apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/) with `--force-conflicts=true` to write resources it manages.

Exceptions:

- The eno-reconciler process can fall back to client-side three-way merge patch by setting `--disable-ssa`
- Merge can be disabled for a resource by setting the `eno.azure.io/replace: "true"` annotation (a full `update` request will be used instead of a `patch`)
- All updates can be disabled for a resource by setting the `eno.azure.io/disable-updates: "true"` annotation

## Deletion

Resources are automatically deleted if they are no longer synthesized (returned by the synthesizer) for a given composition, or the composition was deleted.

> Cascading resource cleanup caused by composition deletion can be disabled by setting the `eno.azure.io/deletion-strategy: orphan` annotation on the composition.

## Drift Detection

By default, resources are reconciled when their expected state changes or when `eno-reconciler` restarts.

In some cases it's useful for the Eno reconciler to regularly sync a resource. 
Syncing the resource will correct any drift, re-evaluate any conditional overrides, etc.

```yaml
annotations:
  eno.azure.io/reconcile-interval: "15m" # supports any value parsable by Go's `time.ParseDuration`
```

## Readiness

Readiness checks determine when resources are ready and control reconciliation order using CEL expressions.

### Readiness Expressions

Resources can include [CEL](https://github.com/google/cel-go) expressions used to determine their readiness.

```yaml
annotations:
  # Basic readiness check
  eno.azure.io/readiness: self.status.foo == 'bar'

  # Multiple checks are AND'd together (all must be true)
  # The latest transition time determines the resource's ready timestamp.
  eno.azure.io/readiness-custom: self.status.anotherField == 'ok'

  # Return condition objects to use the precise timestamp from `lastTransitionTime`.
  # Boolean `true` results use the controller's current system time when readiness is first detected.
  eno.azure.io/readiness-condition: self.status.conditions.filter(item, item.type == 'Ready' && item.status == 'True')
```

### Readiness Groups

Assign resources to numbered groups to control reconciliation order:

```yaml
annotations:
  eno.azure.io/readiness-group: "1"
```

#### Behavior

- Resources without `eno.azure.io/readiness-group` default to group `0`
- Lower-numbered groups reconcile first: `-2` → `-1` → `0` → `1` → `2`
- Group `N+1` resources wait until all group `N` resources are ready


## Advanced Concepts

- [Overrides](./overrides.md)
- [Patch](./patch.md)
