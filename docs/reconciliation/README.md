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
- Create and update requests can be disabled for a resource by setting the `eno.azure.io/disable-updates: "true"` annotation

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

## Advanced Concepts

- [Readiness](./readiness.md)
- [Overrides](./overrides.md)
- [Patch](./patch.md)
