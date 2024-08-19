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
