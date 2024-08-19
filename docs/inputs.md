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

If several inputs are expected to transition in lockstep, use this annotation to override the resource version.
Synthesis will only happen once all inputs bound to a particular composition have matching revisions.
Inputs that do not set a revision "fail open" i.e. will not block synthesis.

```yaml
annotations:
  eno.azure.io/revision: "123"
```

Synthesis can also be delayed until an input has time to catch up to the current version of the composition's synthesizer.
This is useful for cases in which the input is written by a controller that reads synthesizers (annotations, etc.).

```yaml
annotations:
  eno.azure.io/synthesizer-revision: "123" # Will block synthesis if < the synthesizer's metadata.generation
```

# Rollouts

Composition changes are resynthesized immediately.
Changes to deferred input resources (`ref.defer == true`) and synthesizers are subject to the global cooldown period.

- All effected compositions are marked as pending resynthesis immediately
- A maximum of one composition pending resynthesis can begin resynthesis per cooldown period
- The next pending composition can start after the cooldown period has expired AND all resynthesis has completed (with success or terminal error) or been retried at least once
