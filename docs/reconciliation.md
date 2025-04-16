# Reconciliation

Once a composition has been synthesized, the resulting resources are reconciled with a running k8s cluster by the `eno-reconciler` process.


## Merge Semantics

By default, Eno uses [server-side apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/) to create and update resources it manages.

Any property set by the synthesizer will be applied during reconciliation, even if it means overwriting changes made by another client.
Other clients can safely populate fields _not_ managed by Eno - unmanaged fields are not modified.
This is the standard behavior of server-side apply with `--force-conflicts=true`.

> Client-side patching is supported by setting `--disable-ssa`. But beware that Eno can only add and update fields. Fields no longer returned from the synthesizer will not be removed.

## Opacity

Eno is designed to treat the resources it manages as completely opaque - it doesn't "understand" their schema, infer dependencies, etc..

There is one exception to this rule: CRDs are always reconciled before CRs of the kind they define.


## Annotations

Eno synthesizers can use special annotations to configure the Eno reconciler.

> Any labels/annotations prefixed with `eno.azure.io/` will not be included in the final materialized/reconciled resource.

### Reconciliation Interval

By default, resources are reconciled when their expected state changes or when `eno-reconciler` restarts.
The `reconcile-interval` annotation can be used to periodically reconcile the resource to correct for drift, etc.

```yaml
annotations:
  eno.azure.io/reconcile-interval: "15m" # supports any value parsable by Go's `time.ParseDuration`
```

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

Designating a resource to be replaced means that updates will use the normaly update endpoint instead of server-side apply.
Like `kubectl replace`, any fields not managed by Eno will be removed.
Useful for resources that logically have a single reader (e.g. CRDs).

```yaml
annotations:
  eno.azure.io/replace: "true"
```

### Orphaning

The `orphan` deletion strategy disables deletion caused by composition deletion.
The resource will still be deleted if it's not included in the latest synthesis, or if a `Patch` resource explicitly deletes it.

```yaml
annotations:
  eno.azure.io/deletion-strategy: orphan
```


## Meta Resources

Synthesizers can emit special resources that provide special Eno functionality without actually existing as API resources on the cluster.

### Patch

Use jsonpatch to modify resources that are not managed by Eno.

```yaml
apiVersion: eno.azure.io/v1
kind: Patch
metadata:
  name: resource-to-be-patched
  namespace: default
patch:
  apiVersion: v1
  kind: ConfigMap
  ops:
    - { "op": "add", "path": "/data/hello", "value": "world" }
```

> Note: the resource will not be created if it doesn't already exist. Similarly, removing the patch pseudo-resource will not cause Eno to delete the resource.

Setting `metadata.deletionTimestamp` to any value will cause the resource to be deleted if it exists.

```yaml
apiVersion: eno.azure.io/v1
kind: Patch
metadata:
  name: resource-to-be-deleted
  namespace: default
patch:
  apiVersion: v1
  kind: ConfigMap
  ops:
    - { "op": "add", "path": "/metadata/deletionTimestamp", "value": "anything" }
```
