# Eno

Eno is a delivery system for Kubernetes configurations.

> Status is very much alpha! Use with caution.

## Goals

- High performance resource sync
- No scaling bottlenecks within reason (50,000+ resources per cluster)
- Decoupled from any particular templating engine (Helm, Kustomize, etc.)

## Minimal Example

```yaml
# Compositions represent a deployment of a given configuration as specified by its Synthesizer.
apiVersion: eno.azure.io/v1
kind: Composition
metadata:
  name: test-comp
spec:
  synthesizer:
    name: test-synth

---

# Synthesizers specify desired state configurations using the standard KRM Function API.
apiVersion: eno.azure.io/v1
kind: Synthesizer
metadata:
  name: test-synth
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
            "data":{"someKey":"someVal"},
            "kind":"ConfigMap",
            "metadata":{
              "name":"some-config",
              "namespace": "default",
            }
          }
        ]
      }'
```

## Features

### Drift Detection

Resources can be sync'd at a given interval to correct for any configuration drift by setting the annotation `eno.azure.io/reconcile-interval` to a value parsable by Go's `time.ParseDuration`.

## Development Environment

```bash
# Assumes kubectl is configured for your dev cluster (local or otherwise), and can push/pull images from $REGISTRY
export REGISTRY="your registry"
./dev/build.sh
```

## Contributing

This project welcomes contributions and suggestions.  Most contributions require you to agree to a
Contributor License Agreement (CLA) declaring that you have the right to, and actually do, grant us
the rights to use your contribution. For details, visit https://cla.opensource.microsoft.com.

When you submit a pull request, a CLA bot will automatically determine whether you need to provide
a CLA and decorate the PR appropriately (e.g., status check, comment). Simply follow the instructions
provided by the bot. You will only need to do this once across all repos using our CLA.

This project has adopted the [Microsoft Open Source Code of Conduct](https://opensource.microsoft.com/codeofconduct/).
For more information see the [Code of Conduct FAQ](https://opensource.microsoft.com/codeofconduct/faq/) or
contact [opencode@microsoft.com](mailto:opencode@microsoft.com) with any additional questions or comments.

## Trademarks

This project may contain trademarks or logos for projects, products, or services. Authorized use of Microsoft 
trademarks or logos is subject to and must follow 
[Microsoft's Trademark & Brand Guidelines](https://www.microsoft.com/en-us/legal/intellectualproperty/trademarks/usage/general).
Use of Microsoft trademarks or logos in modified versions of this project must not cause confusion or imply Microsoft sponsorship.
Any use of third-party trademarks or logos are subject to those third-party's policies.
