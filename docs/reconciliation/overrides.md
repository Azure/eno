# Overrides

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

This is commonly used to make a subset of properties managed by Eno optional i.e. allow other clients to override them.
For example:

```json
{ "path": "self.data.foo", "value": "default value", "condition": "!has(self.data.foo)" }
```

It's also possible to access composition metadata in condition expressions.

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.spec.cleaningUp", "value": true, "condition": "composition.metadata.deletionTimestamp != null" }
    ]
```

Conditions can match on the ownership status of the field matched by `path`.
This is useful for dropping particular fields when another field manager has set a value.

```yaml
annotations:
  eno.azure.io/overrides: |
    [
      { "path": "self.data.foo", "value": null, "condition": "has(self.data.foo) && !pathManagedByEno" }
    ]
```

## Path Expression Syntax

Overrides use a CEL-like syntax to reference properties.

- `field.anotherfield`: Traverse object fields
- `field["key"]` or `field['key']`: Access object fields by key (supports any field name including hyphens)
- `field[2]`: Access array elements by index
- `field[*]`: Match all elements in an array
- `field[someKey="value"]`: Match array elements by a key-value pair

Paths can be chained, e.g., `self.field.anotherfield[2].yetAnotherField`.
If any segment of the path is nil or missing, the override will not be applied.