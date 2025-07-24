# Symphony

Symphonies are higher-order units of configuration that involve multiple synthesizers.

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

## Bindings

Compositions that are part of the same symphony can share common bindings.

> Note: refs require matching bindings but bindings don't require matching refs. So a symphony can set all possible bindings and synthesizers can define a matching ref only if the input is needed.

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

### Overrides

Variations can override and append to the inherited bindings.

If overrides are used for most synthesizers, that's a good sign that the symphony pattern doesn't fit your use-case.

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
