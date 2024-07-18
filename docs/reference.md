# Reference Docs

## Composition Basics

The most basic composition includes only a reference to its synthesizer.

```yaml
apiVersion: eno.azure.io/v1
kind: Composition
metadata:
  name: example
spec:
  synthesizer:
    name: test-synth
```

This example will cause a "synthesis" of the composition through its synthesizer
and track the lifecycle of the resulting resources:
deleting the composition will delete the resources,
updating it will cause resynthesis, etc.

### Deletion Modes

To keep any resources created because of a composition,
Eno supports an orphaning deletion mode by setting this annotation on the composition resource:

```yaml
eno.azure.io/deletion-strategy: orphan
```

## Synthesizer Basics

Synthesizers are container images that have one or more implementations of the [KRM Functions API](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).

For example, one could implement the basics of the API using bash.

```yaml
apiVersion: eno.azure.io/v1
kind: Synthesizer
metadata:
  name: example
spec:
  image: docker.io/ubuntu:latest
  command:
  - /bin/bash
  - -c
  - |
    echo '
      {
        "apiVersion":"config.kubernetes.io/v1",
        "kind":"ResourceList",
        "items":[
          {
            "apiVersion":"v1",
            "kind":"ConfigMap",
            "metadata":{
              "name":"some-config",
              "namespace": "default"
            }
          }
        ]
      }'
---
```

### Error Handling

The synthesizer process's `stderr` is piped to the synthesizer container it's running in,
so any typical log forwarding infra can be used.

The KRM API supports "results", which are a kind of metadata related to the execution process that would not otherwise be reflected in the generated resources.
Each result has a severity of info, warning, or error.

Functions that produce one or more results with severity == error will cause the resulting synthesis to be marked as having failed.
Failed syntheses are not reconciled or retried.

### Merge Semantics / Drift Detection

Normally any change to resources produced by the synthesizer will be reconciled into the corresponding API resources and any drift will be corrected.

Eno uses strategic three-way merge when possible, falling back to non-strategic three-way merge otherwise.
This means other clients can set properties not managed by Eno,
but any changes to Eno-managed properties are considered configuration drift and will be reconciled back to the expected state.

By default, configuration drift will only be corrected when the expected state changes or the Eno reconciler restarts.
In most cases, resources produced by synthesizers should include an annotation to specify an interval at which drift can be corrected.

```yaml
# supports any value parsable by Go's `time.ParseDuration`
eno.azure.io/reconcile-interval: "15m"
```

In cases where resources are expected to be modified by other clients, drift detection and updates can be disabled by setting this annotation on resources produced by synthesizers:

```yaml
eno.azure.io/disable-updates: "true"
```

## Readiness

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

Readiness expressions are set in the `eno.azure.io/readiness` annotation of resources produced by synthesizers.
If more than one expression is needed, arbitrarily-named annotations sharing that prefix are alaso supported i.e. `eno.azure.io/readiness-foo`.
They are logically AND'd.

Resources that do not have a readiness expression will become ready immediately after reconciliation.

### Ordering

Resources produced by synthesizers can set this annotation to order their own reconciliation relative to other resources in the same composition.

```yaml
eno.azure.io/readiness-group: 1
```

The default group is 0, and lower numbers are reconciled first.
So the example above will cause its resource to not be reconciled until all resources without a readiness group have become ready.

# Inputs

Eno supports passing any Kubernetes resources to synthesizers as inputs.

Synthesizers can expose `refs` like:

```yaml
apiVersion: eno.azure.io/v1
kind: Synthesizer
spec:
  refs:
    - key: foo
      resource:
        group: ""
        version: v1
        kind: "ConfigMap"
```

In this case, compositions must have a `binding` to the input `foo`.

```yaml
apiVersion: eno.azure.io/v1
kind: Composition
spec:
  bindings:
    - key: foo
      resource:
        name: test-input
        namespace: default
```

The composition will be resynthesized whenever `test-input`'s `resourceVersion` changes by default, subject to the global cooldown period.

It's possible to defer resynthesis until some other inputs have been modified.
This is useful for cases in which another process updates several input resources serially.

Setting this annotation to a higher value than any other input of a given composition will not result in resynthesis until all other inputs have the same value.

```yaml
eno.azure.io/revision: 123
```

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


# Rollouts

Composition changes are resynthesized immediately.
Changes to deferred input resources (`ref.defer == true`) and synthesizers are subject to the global cooldown period.

- All effected compositions are marked as pending resynthesis immediately
- A maximum of one composition pending resynthesis can begin resynthesis per cooldown period
- The next pending composition can start after the cooldown period has expired AND all resynthesis has completed (with success or terminal error) or been retried at least once


## Symphony Basics

Symphonies are units of configuration that involve multiple synthesizers.
Essentially they represent sets of compositions that share most properties.

```yaml
apiVersion: eno.azure.io/v1
kind: Symphony
metadata:
  name: basic-symphony
spec:
  variations:
    - synthesizer:
        name: synth-1
    - synthesizer:
        name: synth-2
```

This will result in the creation of two compositions owned by the symphony.
Removing a variation will cause the corresponding composition to be deleted.

### Bindings

The compositions can share common bindings.

```yaml
apiVersion: eno.azure.io/v1
kind: Symphony
metadata:
  name: basic-symphony
spec:
  bindings:
    - key: foo
      resource:
        name: test-input
        namespace: default
  variations:
    - synthesizer:
        name: synth-1
    - synthesizer:
        name: synth-2
```

Variations can override the inherited bindings.
This example will override the `foo` binding to reference a different resource for `synth-2`.

```yaml
apiVersion: eno.azure.io/v1
kind: Symphony
metadata:
  name: basic-symphony
spec:
  bindings:
    - key: foo
      resource:
        name: test-input
        namespace: default

  variations:
    - synthesizer:
        name: synth-1
      # Override an existing binding
      bindings:
        - key: foo
          resource:
            name: a-different-input
            namespace: default

    - synthesizer:
        name: synth-2
      # Append a second binding
      bindings:
        - key: bar
          resource:
            name: a-different-input
            namespace: default
```
