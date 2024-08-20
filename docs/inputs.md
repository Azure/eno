# Inputs

Eno supports passing Kubernetes resources to synthesizers as inputs.

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

Compositions that use this synthesizer need to "bind" the ref by providing the input object's name and namespace.

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

The composition will be resynthesized whenever `test-input`'s `resourceVersion` changes.

## Revisions

Use this annotation when several inputs are expected to transition in lockstep.
Synthesis will only happen once all objects bound to the composition have matching revisions.

This pattern is useful when inputs are coupled in such a way that the synthesizer may behave unexpectedly during state transitions.
Essentially it forms a simple transaction layer on top of the Kubernetes API to provide atomicity across resources.

> Note: Inputs that do not set a revision "fail open" i.e. will not block synthesis.

```yaml
annotations:
  eno.azure.io/revision: "123"
```

## Synthesizer Revisions

In more complex use-cases controllers outside of Eno may manage input resources based on the annotations of `Synthesizer` objects.
This pattern allows synthesizers to "request" certain inputs dynamically without modifying the controller.

To simplify this pattern, Eno supports an annotation that can be used by other controllers to signal that an input resource has caught up to a particular synthesizer generation.

```yaml
annotations:
  eno.azure.io/synthesizer-revision: "123" # Will block synthesis if < the synthesizer's metadata.generation
```

## Rollouts

A cluster-wide cooldown period used to space out synthesizer changes across compositions is defined by the controller's `--rollout-cooldown` flag.
No more than one composition for a given synthesizer will be updated per cooldown period.

The idea is to leave adequate time for other systems to detect and roll back bad configurations before they infect other compositions.

Synthesizers can opt-in to also honor the cooldown period for specific inputs by setting `defer: true` on the ref.
This is useful for inputs that are shared between many compositions, similar to synthesizers.

> Note: if a synthesis honoring the cooldown fails, Eno will move onto the next period after one retry.
