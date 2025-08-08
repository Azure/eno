# Symphony

> ⚠️ This is an advanced Eno concept, most use-cases can be addressed by managing individual compositions.

Symphonies are higher-order units of configuration that involve multiple synthesizers.

```yaml
apiVersion: eno.azure.io/v1
kind: Symphony
metadata:
  name: basic-symphony
spec:
  variations:
    # Define a minimal composition that is part of the symphony.
    - synthesizer:
        name: synth-1

    # Define a full composition with every supported property.
    # Values are copied opaquely to the resulting composition.
    - synthesizer:
        name: synth-2
      synthesisEnv:
        - name: EXAMPLE_ENVVAR
          value: "example-value"
      bindings:
      - key: foo
        resource:
          name: test-input
          namespace: default

      # Labels/annotations can also be set on compositions.
      # Values are additively merged into the existing set.
      # Setting a value to an empty string will cause it to be removed.
      annotations: {}
      labels: {}
```

## Deletion

Compositions that were created by symphonies have special deletion behavior.

- Removing a variation deletes the composition using the standard composition deletion logic: hold a finalizer until all of the composition's resources have been cleaned up
- Deleting a symphony propagates to its compositions while short circuiting the finalizer - they're deleted immediately without waiting for resource cleanup

## Annotation Reconciliation Protocol

Symphonies update annotations across their compositions using a two-phase commit.

- First: annotations that should no longer be set are pruned
- Second: annotations that are missing or incorrect are updated

This approach provides a kind of "at-most-once" semantics: it's possible to set a value on __at most__ one composition in a symphony using a single write to the symphony object.

### Example

Migrate a resource from one synthesizer to another with low risk of conflicting/oscillating updates.

- Set an annotation to a positive state (e.g. `"true"`) on one variation
- Ensure the annotation is missing from the other composition by setting it to an empty string in the variation
- Update both synthesizers to use overrides that conditionally skip reconciliation of some resource(s) based on the state of the annotation
- At any time, update the symphony to "flip" the annotation values and pass off ownership between the two compositions
  - Since Eno removes the annotation first, it will only exist on (at most) one composition
