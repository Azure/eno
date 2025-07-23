# Eno

Compose Kubernetes deployments.

- 🎹 **Synthesize**: generate manifests dynamically in short-lived pods
- ♻️ **Reconcile**: apply the generated configurations and rapidly correct any drift
- 🏃‍➡️ **React**: re-synthesize when input resources are modified

## What can Eno do?

- Magically regenerate configurations when their inputs change
- Safely roll out changes that impact many instances of a configuration
- Support deployments larger than apiserver's 1.5MB resource limit
- Define complex ordering relationships between resources
- Patch resources without taking full ownership
- Use custom CEL expressions to determine the readiness of resources

## Docs

- [Reconciliation](./docs/reconciliation.md)
- [Synthesis](./docs/synthesis.md)
- [Symphony](./docs/symphony.md)
- [Synthesizer API](./docs/synthesizer-api.md)
- [Generated API Docs](./docs/api.md)

## Getting Started

### 1. Install Eno

```bash
export TAG=$(curl https://api.github.com/repos/Azure/eno/releases | jq -r '.[0].name')
kubectl apply -f "https://github.com/Azure/eno/releases/download/${TAG}/manifest.yaml"
```

### 2. Create a Synthesizer

Synthesizers reference a container image that implements a [KRM function](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).
This example uses a small bash script, but you will probably want to use `github.com/Azure/eno/pkg/function`.

```bash
kubectl apply -f - <<YAML
apiVersion: eno.azure.io/v1
kind: Synthesizer
metadata:
  name: getting-started
  namespace: default
spec:
  image: docker.io/ubuntu:latest
  refs:
    - key: config
      resource:
        group: "" # core
        version: v1
        kind: ConfigMap
  command:
  - /bin/bash
  - -c
  - |
    # Read inputs from stdin
    replica_count=\$(sed -n 's/.*"replicas":"\([^"]*\)".*/\1/p')

    # Write the resulting KRM resource list to stdout
    echo '{
      "apiVersion":"config.kubernetes.io/v1",
      "kind":"ResourceList",
      "items":[
        {
          "apiVersion":"apps/v1",
          "kind":"Deployment",
          "metadata":{
            "name":"my-app",
            "namespace": "default"
          },
          "spec": {
            "replicas": REPLICA_COUNT,
            "selector": { "matchLabels": { "app": "getting-started" } },
            "template": {
              "metadata": { "labels": { "app": "getting-started" } },
              "spec": {
                "containers": [{ "name": "svc", "image": "nginx" }]
              }
            }
          }
        }
      ]
    }' | sed "s/REPLICA_COUNT/\$replica_count/g"
YAML
```

### 3. Create a Composition

Compositions bind a unique set of inputs to a synthesizer and manage the lifecycle of the resulting configuration.

```bash
kubectl apply -f - <<YAML
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: my-first-config
    namespace: default
  data:
    replicas: "1"
---
  apiVersion: eno.azure.io/v1
  kind: Composition
  metadata:
    name: my-first-composition
    namespace: default
  spec:
    synthesizer:
      name: getting-started
    bindings:
      - key: config
        resource:
          name: my-first-config
          namespace: default
YAML
```

Eno will execute the synthesizer in a short-lived pod and create the resulting resource(s).

```bash
$ kubectl get composition
NAME                   SYNTHESIZER       AGE   STATUS   ERROR
my-first-composition   getting-started   0s    Ready

$ kubectl get deploy my-app
NAME     READY   UP-TO-DATE   AVAILABLE   AGE
my-app   1/1     1            1           0s
```

### 4. Resynthesize

Eno will automatically resynthesize the composition when its inputs change.

```bash
kubectl patch configmap my-first-config --patch '{"data":{"replicas":"2"}}'

$ kubectl get deploy my-app
NAME     READY   UP-TO-DATE   AVAILABLE   AGE
my-app   2/2     2            2           2m
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
