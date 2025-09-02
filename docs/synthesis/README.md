# Writing Synthesizers

Synthesizers are containerized programs that transform input Kubernetes resources into output resources using a simple JSON protocol over stdin/stdout. They enable declarative resource generation within Eno compositions.

## What is Synthesis?

Synthesis is the process where Eno runs your containerized synthesizer in a short-lived pod to generate Kubernetes resources. Your synthesizer receives input resources via stdin as JSON and returns generated resources via stdout, also as JSON.

## Quick Start

The fastest way to get started is with one of our language-specific libraries:

- **Go**: Use the [Go synthesizer library](./examples/02-go-synthesizer/main.go)
- **Helm**: Use the [Helm shim](./examples/03-helm-shim) 
- **KCL**: Use the [KCL library](./pkg/kclshim/)
- **Any language**: Implement the [JSON protocol](#protocol) directly

Your synthesizer needs to:
1. Read a JSON ResourceList from stdin
2. Process the input resources 
3. Write a JSON ResourceList with generated resources to stdout


## When Synthesis Runs

Eno automatically triggers synthesis when any of these conditions occur:

- **Composition changes**: The composition resource itself is modified
- **Synthesizer changes**: The synthesizer container image or configuration changes  
- **Input changes**: Any input resource bound to the composition changes

### Controlling Synthesis Timing

#### Deferral for Large-Scale Changes

Some changes can affect many compositions simultaneously. These are marked as "deferred" to prevent overwhelming your cluster:

- **Synthesizer updates**: Changes to synthesizer images/config
- **Deferred inputs**: Input resources with `defer: true` in their binding

Deferred changes use a global cooldown period (configurable with `--rollout-cooldown`) to stagger synthesis across compositions.

**Opting out of deferred synthesis:**
```yaml
metadata:
  annotations:
    eno.azure.io/ignore-side-effects: "true"
```
With this annotation, only direct composition changes will trigger synthesis.

#### Input Synchronization 

Sometimes you need multiple input resources to be synchronized before synthesis runs.

**Revision-based synchronization:**
```yaml
# On input resources
metadata:
  annotations:
    eno.azure.io/revision: "123"
```
Synthesis waits until all inputs have the same revision number.

**Generation-based synchronization:**
```yaml
# On input resources  
metadata:
  annotations:
    # Wait for synthesizer to reach generation 123
    eno.azure.io/synthesizer-generation: "123"
    
    # Wait for composition to reach generation 321
    eno.azure.io/composition-generation: "321"
```
This blocks synthesis until input controllers have "seen" the requested synthesizer/composition state.


## Synthesis Protocol

Your synthesizer communicates with Eno using JSON over stdin/stdout, following the [KRM Functions Specification](https://kustomize.io/).

> 💡 **Recommendation**: Use our language-specific libraries ([Go](./examples/02-go-synthesizer/main.go), [Helm](./examples/03-helm-shim), [KCL](./pkg/kclshim/)) instead of implementing the protocol directly.

### Input Format

Eno sends input resources as a JSON ResourceList via stdin:

```json
{
  "apiVersion": "config.kubernetes.io/v1",
  "kind": "ResourceList",
  "items": [
    {
      "apiVersion": "v1",
      "kind": "ConfigMap",
      "metadata": {
        "name": "my-app-config",
        "annotations": {
          "eno.azure.io/input-key": "app-config"
        }
      },
      "data": {
        "replicas": "3",
        "image": "nginx:1.21"
      }
    }
  ]
}
```

The `eno.azure.io/input-key` annotation identifies which input binding this resource came from.

### Output Format

Return generated resources using the same ResourceList format via stdout:

```json
{
  "apiVersion": "config.kubernetes.io/v1", 
  "kind": "ResourceList",
  "items": [
    {
      "apiVersion": "apps/v1",
      "kind": "Deployment",
      "metadata": {
        "name": "my-app"
      },
      "spec": {
        "replicas": 3,
        "selector": {
          "matchLabels": {"app": "my-app"}
        },
        "template": {
          "metadata": {
            "labels": {"app": "my-app"}
          },
          "spec": {
            "containers": [{
              "name": "app",
              "image": "nginx:1.21"
            }]
          }
        }
      }
    }
  ]
}
```

### Error Handling

Report errors using the `results` field. The first error will appear in the composition status:

```json
{
  "apiVersion": "config.kubernetes.io/v1",
  "kind": "ResourceList", 
  "results": [
    {
      "severity": "error",
      "message": "Invalid configuration: replicas must be a positive integer"
    }
  ]
}
```

This error will be visible when checking composition status:
```bash
$ kubectl get compositions
NAME      SYNTHESIZER     AGE   STATUS     ERROR
my-app    my-synthesizer  10s   NotReady   Invalid configuration: replicas must be a positive integer
```
