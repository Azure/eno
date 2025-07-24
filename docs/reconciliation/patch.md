# Patch

> ⚠️ Most use-cases don't need Patch resources. Only use them if you're sure it's the right option!

Patch resources Eno to modify or delete resources that it doesn't manage.

The `Patch` kind isn't an API registered with the Kubernetes apiserver. Patches a kind of "meta-resource" used to configure Eno.
They are not accessible to other clients.


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

> Note: the resource will not be created if it doesn't already exist. Similarly, removing the patch will not cause Eno to delete the resource.

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
