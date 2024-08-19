# Pseudo-Resources

## Patch

Synthesizers can produce resources of a special kind to modify resources not managed by Eno.

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

The resource will not be created if it doesn't already exist.
Removing the patch pseudo-resource will not cause Eno to delete the resource.

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

### Deletion Modes

To keep any resources created because of a composition,
Eno supports an orphaning deletion mode by setting this annotation on the composition resource:

```yaml
eno.azure.io/deletion-strategy: orphan
```