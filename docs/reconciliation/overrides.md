# Overrides

> ⚠️ This is an advanced Eno concept

Overrides modify specific fields of a resource during reconciliation without requiring resynthesis.
They apply conditional modifications on top of synthesized resources using CEL expressions evaluated against the resource's current state.

Overrides are applied during reconciliation, so in most cases they should be used alongside `eno.azure.io/reconcile-interval`.

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.data.foo", "value": "new conditional value", "condition": "self.data.bar == 'baz'" }
    ]
```

## Path Syntax

Reference resource properties using these path expressions:

- `field.anotherfield`: Traverse object fields
- `field["key"]` or `field['key']`: Access object fields by key (supports any field name including hyphens)
- `field[2]`: Access array elements by index
- `field[*]`: Match all elements in an array
- `field[someKey="value"]`: Match array elements by a key-value pair

Chain path segments like: `self.field.anotherfield[2].yetAnotherField`. Overrides are skipped gracefully if any path segment has a nil value.


## Composition Metadata

CEL expressions can access metadata from the resource's associated `Composition`:

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.spec.cleaningUp", "value": true, "condition": "composition.metadata.deletionTimestamp != null" }
    ]
```

Supported fields:

- `composition.metadata.name`
- `composition.metadata.namespace`
- `composition.metadata.labels`
- `composition.metadata.annotations`

## Overriding Annotations

Override these Eno annotations to modify `eno-reconciler` behavior at runtime:

> The behavior of the annotations are documented separately

- `eno.azure.io/disable-updates`
- `eno.azure.io/replace`
- `eno.azure.io/reconcile-interval`

## Field Manager

Use `pathManagedByEno` to check if Eno manages a field. This prevents conflicts by conditionally unsetting fields only when another controller manages them.

This example sets `data.foo` to null only when the field exists but isn't managed by Eno:

> ⚠️ Setting a value to null causes Eno to omit it. If Eno currently manages the field, omitting it would cause apiserver to prune the value. But here we only nullify when `!pathManagedByEno`, preserving values managed by other controllers.

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno" }
    ]
```

Use the same path structure as Kubernetes `metadata.managedFields`. Arrays are typically indexed by key, not numeric index:

- ✅ `self.spec.template.spec.containers[name='myContainer'].image`
- ❌ `self.spec.template.spec.containers[0].image`

## Resource Quantity Comparisons

Use `compareResourceQuantities()` to compare Kubernetes resource quantity strings like `resources.limits.cpu` values:

- Returns `0` when values are equal
- Returns `-1` when left < right  
- Returns `1` when left > right

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      {
        "path": "self.spec.resources.requests.memory",
        "value": "2Gi",
        "condition": "compareResourceQuantities(self.spec.resources.requests.memory, '1Gi') < 0"
      }
    ]
```
