# Readiness

## Readiness Expressions

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

## Readiness Groups

Resources produced by synthesizers can set this annotation to order their own reconciliation relative to other resources in the same composition.

```yaml
annotations:
  eno.azure.io/readiness-group: "1"
```

The default group is 0 and lower numbers are reconciled first.
So the example above will cause its resource to not be reconciled until all resources without a readiness group have become ready.
