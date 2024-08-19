

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

### Deletion Behavior

Symphonies are high-level resources designed to always converge, even in the face of rare split-brain states.

Force deleting namespaces leaves resources in a strange state in which they exist but cannot be updated.
Symphonies recover from this state by carefully recreating the namespace and forcibly removing internal finalizers.
Because of this it's possible that managed resources will not be cleaned up if they exist outside of the orphaned namespace.
Worst case, managed resources might be recreated by the Eno reconciler if it outpaces kube-controller-manager's namespace controller.
Beware of this if you plan to use symphony resources to manage resources outside of the symphony's own namespace.