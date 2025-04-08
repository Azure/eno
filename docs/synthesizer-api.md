# Synthesizer API

Synthesizers are container images that implement the [KRM Functions API](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).


## SDK

Eno provides a simple library for writing synthesizers in Go: [github.com/Azure/eno/pkg/function](https://pkg.go.dev/github.com/Azure/eno/pkg/function).


## IO

Synthesizers communicate with Eno through stdin/stdout.

### Inputs

Input resources are provided to the synthesizer through a json object streamed over stdin.

Example:

```json
{
  "apiVersion":"config.kubernetes.io/v1",
  "kind":"ResourceList",
  "items": [{
    "apiVersion": "v1",
    "kind": "ConfigMap",
    "metadata": {
      "name": "my-app-config",
      "annotations": {
        "eno.azure.io/input-key": "value-from-synthesizer-ref"
      }
    }
  }]
}
```

### Outputs

The results of a synthesizer run should be returned through stdout using the same schema as the inputs:

```json
{
  "apiVersion":"config.kubernetes.io/v1",
  "kind":"ResourceList",
  "items": [{
    "apiVersion": "apps/v1",
    "kind": "Deployment",
    // ...
  }]
}
```

The first error result is visible when listing compositions.

```json
{
  "apiVersion":"config.kubernetes.io/v1",
  "kind":"ResourceList",
  "results": [{
    "severity": "error",
    "message": "The system is down, the system is down"
  }]
}
```

For example:

```bash
$ kubectl get compositions
NAME      SYNTHESIZER     AGE   STATUS     ERROR
example   error-example   10s   NotReady   The system is down, the system is down
```

### Logging

The synthesizer process's `stderr` is piped to the synthesizer container it's running in so any typical log forwarding infra can be used.
