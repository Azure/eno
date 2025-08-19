# Patch

> ⚠️ This is an advanced Eno concept

Patch resources modify or delete existing Kubernetes resources that Eno doesn't manage.
They apply [JSON patches](https://tools.ietf.org/html/rfc6902) to existing resources.

Patches are meta-resources for Eno configuration and are not registered with the Kubernetes API server.


## Modifying Resources

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

> ⚠️ Patches only modify existing resources. If the target resource doesn't exist, the patch is ignored. Removing the patch does not revert changes or delete the resource.

## Deleting Resources

To delete a resource, set its `metadata.deletionTimestamp` field:

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

> The deletion happens immediately when the patch is applied, regardless of the value specified.

## Patch Operations

Use [RFC 6902 JSON Patch](https://tools.ietf.org/html/rfc6902) operations in the `ops` array:

- `add`: Add a field or array element
- `remove`: Remove a field or array element
- `replace`: Replace a field value
- `move`: Move a field to a new location
- `copy`: Copy a field to a new location

## Conditional Patches

Patches support the [`eno.azure.io/overrides` annotation](overrides.md) for conditional logic:

```yaml
apiVersion: eno.azure.io/v1
kind: Patch
metadata:
  name: conditional-patch
  namespace: default
  annotations:
    eno.azure.io/overrides: |
      [{
        "path": "self.patch.ops",
        "condition": "composition.metadata.deletionTimestamp != null",
        "value": [{ "op": "add", "path": "/metadata/deletionTimestamp", "value": "not-null" }]
      }]
patch:
  apiVersion: v1
  kind: ConfigMap
  ops: []
```

This patch only deletes the ConfigMap when the parent Composition is being deleted.

## Ordering with Readiness Groups

Use the `eno.azure.io/readiness-group` annotation to control when patches are applied relative to other resources:

```yaml
apiVersion: eno.azure.io/v1
kind: Patch
metadata:
  name: pre-creation-cleanup
  namespace: default
  annotations:
    eno.azure.io/readiness-group: "1"
patch:
  apiVersion: v1
  kind: ConfigMap
  ops:
    - { "op": "add", "path": "/metadata/deletionTimestamp", "value": "not-null" }

---

apiVersion: v1
kind: ConfigMap
metadata:
  name: my-configmap
  namespace: default
  annotations:
    eno.azure.io/readiness-group: "2"
data:
  foo: bar
```

This ensures the patch (group 1) deletes any existing ConfigMap before the new one (group 2) is created.
