# Overrides

> ⚠️ This is an advanced Eno concept

Overrides let you modify specific fields of a resource during reconciliation.
These modifications are applied on top of the synthesized resource and can be conditional.
Conditions are CEL expressions that are evaluated against the current state of the resource at reconciliation time.

This allows Eno synthesizers to specify basic runtime behavior without requiring resynthesis.

> Overrides are applied during reconciliation, so in most cases they should be used alongside `eno.azure.io/reconcile-interval`.

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.data.foo", "value": "new conditional value", "condition": "self.data.bar == 'baz'" }
    ]
```

## Path Expression Syntax

Overrides use a simple syntax to reference properties.

- `field.anotherfield`: Traverse object fields
- `field["key"]` or `field['key']`: Access object fields by key (supports any field name including hyphens)
- `field[2]`: Access array elements by index
- `field[*]`: Match all elements in an array
- `field[someKey="value"]`: Match array elements by a key-value pair

Paths can be chained, e.g., `self.field.anotherfield[2].yetAnotherField`.
If any segment of the path is nil or missing, the override will not be applied.


## Composition Metadata

Some metadata of the resource's associated `Composition` resource is available to the condition's CEL expression.

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

Certain Eno annotations can be overridden to modify the behavior of `eno-reconciler` at runtime.

> The behavior of the annotations are documented elsewhere, this list serves only to document which can be targeted by overrides.

- `eno.azure.io/disable-updates`
- `eno.azure.io/replace`
- `eno.azure.io/reconcile-interval`

## Field Manager

Conditions can check if the field matched by the override's `path` is currently managed by Eno according to the object's `metadata.managedFields`.
The most common use-case is conditionally "unsetting" fields in order to avoid stomping on expected changes from other controllers.

This example causes the `data.foo` field to only be set by Eno when the field is empty or already managed by Eno.

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno" }
    ]
```

### Caveats

Eno looks up the manager of the field specified by `path` using internal Kubernetes libraries that read directly from `metadata.managedFields`.
So it's important to reference paths using the same structure as the managed fields metadata.

For example: most arrays are indexed by key - not numeric index.

- ✅ `self.spec.template.spec.containers[name='myContainer'].image`
- ❌ `self.spec.template.spec.containers[0].image`

## Kubernetes Resource Quantity Comparisons

Eno `cel` expressions support a special function for comparing Kubernetes resource quantity strings.
For example: the string representation of values in a container's `resources.limits.cpu`.

- Returns 0 when values are equal
- Returns -1 when left < right
- Returns 1 when left > right

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