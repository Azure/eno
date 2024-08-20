# Advanced Synthesis

## Deletion Modes

Eno will delete all resources associated with a composition when it's deleted.
In unusual cases where the resources should be preserved, a special annotation can be set on the composition before it's deleted:

```yaml
annotations:
  eno.azure.io/deletion-strategy: orphan
```

## Patch Unmanaged Resources

Synthesizers can generate special "pseudo resources" to modify objects not managed by Eno.

Standard jsonpatch operations are supported.

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
