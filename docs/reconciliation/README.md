# Reconciliation

Synthesized compositions are reconciled into real k8s resources by the `eno-reconciler` process.

## Opacity

Eno is designed to treat the resources it manages as completely opaque - it doesn't "understand" their schema, infer dependencies, etc..

There is one exception to this rule: CRDs are always reconciled before CRs of the kind they define.

## Merge Semantics

By default, Eno uses [server-side apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/) with `--force-conflicts=true` to write resources it manages.

> Client-side patching is supported by setting `--disable-ssa`. But beware that Eno can only add and update fields. Fields no longer returned from the synthesizer will not be removed.

## Deletion

Resources are automatically deleted if they are no longer synthesized (returned by the synthesizer) for a given composition.

> This can be disabled by setting the `eno.azure.io/deletion-strategy: orphan` annotation on the composition.

## Drift Detection

By default, resources are reconciled when their expected state changes or when `eno-reconciler` restarts.

In some cases it's useful for the Eno reconciler to regularly sync a resource. 
Syncing the resource will correct any drift, re-evaluate any conditional overrides, etc.

```yaml
annotations:
  eno.azure.io/reconcile-interval: "15m" # supports any value parsable by Go's `time.ParseDuration`
```

## Annotations

Eno synthesizers can use special annotations to configure the Eno reconciler.

> Any labels/annotations prefixed with `eno.azure.io/` will not be included in the final materialized/reconciled resource.

### Readiness Expressions

Resources can include [CEL](https://github.com/google/cel-go) expressions used to determine their readiness.
Readiness signal is reflected in the status of the corresponding composition and can be used to order other resource operations.

```yaml
annotations:
  eno.azure.io/readiness: self.status.foo == 'bar'

  # Any expressions with the `readiness-*` prefix are logically AND'd
  eno.azure.io/readiness-foo: self.status.anotherField == 'ok'

  # Returning a condition object causes Eno to use its last transition time as the readiness timestamp, otherwise it uses the eno-reconciler pod's system time
  eno.azure.io/readiness-condition: self.status.conditions.filter(item, item.type == 'Test' && item.status == 'False')
```

### Readiness Groups

Resources produced by synthesizers can set this annotation to order their own reconciliation relative to other resources in the same composition.

```yaml
annotations:
  eno.azure.io/readiness-group: "1"
```

The default group is 0 and lower numbers are reconciled first.
So the example above will cause its resource to not be reconciled until all resources without a readiness group have become ready.

### Disable Updates

Disabling updates means Eno will create the resource when missing, delete it when no longer part of an active composition, but never update it in any way.

```yaml
annotations:
  eno.azure.io/disable-updates: "true"
```

### Replace

Designating a resource to be replaced means that updates will use the normal update endpoint instead of server-side apply.
Like `kubectl replace`, any fields not managed by Eno will be removed.
Useful for resources that logically have a single reader (e.g. CRDs).

```yaml
annotations:
  eno.azure.io/replace: "true"
```
