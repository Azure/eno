# Readiness and Ordering

## CEL Expressions

Resources can include expressions used to determine their readiness.
Readiness signal is reflected in the status of the corresponding composition and can be used to order other resource operations.

Expressions use [CEL](https://github.com/google/cel-go).
Their evaluation "latches" i.e. once a resource becomes ready it cannot be non-ready again until its expected configuration changes. 

Readiness expressions can return either bool or a Kubernetes condition struct. If a condition is returned it will be used as the resource's readiness time, otherwise the controller will use wallclock time at the first moment it noticed the truthy value. When possible, match on a timestamp to preserve accuracy.

Example matching on typical status conditions:

```cel
self.status.conditions.filter(item, item.type == 'Test' && item.status == 'False')
```

Example matching on a boolean:

```cel
self.status.foo == 'bar'
```

## Annotations

Readiness expressions are set in the `eno.azure.io/readiness` annotation of resources produced by synthesizers.

If more than one expression is needed, arbitrarily-named annotations sharing that prefix are alaso supported i.e. `eno.azure.io/readiness-foo`.
They are logically AND'd.

## Reconciliation Ordering

Resources produced by synthesizers can set this annotation to order their own reconciliation relative to other resources in the same composition.

```yaml
annotations:
  eno.azure.io/readiness-group: 1
```

The default group is 0 and lower numbers are reconciled first.
So the example above will cause its resource to not be reconciled until all resources without a readiness group have become ready.

Readiness groups (as the name suggests) honor readiness expressions i.e.
reconciliation will be blocked until the dependency resource has become ready.

> Note: Eno does not infer order from resource kind, so configmaps might not by reconciled before deployments that reference them. One exception: CRDs are always reconciled before CRs of the resource kind they define. 
