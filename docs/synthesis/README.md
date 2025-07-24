# Synthesis

Eno uses short-lived pods to synthesize compositions using [a simple stdio protocol](./synthesizer-api.md).
This process and its results are referred to as `synthesis`.

## Dispatch

Synthesis will occur in these scenarios unless blocked by one of the conditions described later:

- The composition changed
- The composition's synthesizer changed
- An input of the composition changed

### Deferral

Changes that may impact many compositions are designated as `deferred`.
This includes synthesizer changes and changes to any inputs bound to refs that set `defer: true`.

Deferred changes are subject to a global cooldown period to avoid suddenly changing hundreds/thousands/etc. of compositions.
The cooldown period can be configured with `--rollout-cooldown`.

Compositions can opt-out of any deferred syntheses.
Only composition updates will cause synthesis when this annotation is set on the composition.

```yaml
annotations:
  eno.azure.io/ignore-side-effects: "true"
```

### Input Lockstep

Synthesis can be blocked until relevant inputs have the same revision.
This pattern is useful when inputs are coupled in such a way that the synthesizer may behave unexpectedly during state transitions.

> Note: Inputs that do not set a revision "fail open" i.e. will not block synthesis.

```yaml
annotations:
  eno.azure.io/revision: "123"
```

It's also possible to block synthesis until an input has "seen" the current synthesizer/composition resource.
This is useful in cases where another controller generates input resources based on some properties or annotations of the synthesizer/composition.

```yaml
annotations:
  # Will block synthesis if < the synthesizer's metadata.generation
  eno.azure.io/synthesizer-generation: "123"

  # Will block synthesis if < the composition's metadata.generation
  eno.azure.io/composition-generation: "321"
```
