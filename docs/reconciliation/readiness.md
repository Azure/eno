# Readiness

Readiness checks determine when resources are ready and control reconciliation order using CEL expressions.

## Readiness Expressions

Resources can include [CEL](https://github.com/google/cel-go) expressions used to determine their readiness.

```yaml
annotations:
  # Basic readiness check
  eno.azure.io/readiness: self.status.foo == 'bar'

  # Multiple checks are AND'd together (all must be true)
  # The latest transition time determines the resource's ready timestamp.
  eno.azure.io/readiness-custom: self.status.anotherField == 'ok'

  # Return condition objects to use the precise timestamp from `lastTransitionTime`.
  # Boolean `true` results use the controller's current system time when readiness is first detected.
  eno.azure.io/readiness-condition: self.status.conditions.filter(item, item.type == 'Ready' && item.status == 'True')
```

## Readiness Groups

Assign resources to numbered groups to control reconciliation order:

```yaml
annotations:
  eno.azure.io/readiness-group: "1"
```

### Behavior

- Resources without `eno.azure.io/readiness-group` default to group `0`
- Lower-numbered groups reconcile first: `-2` → `-1` → `0` → `1` → `2`
- Group `N+1` resources wait until all group `N` resources are ready
