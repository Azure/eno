# Eno

Dynamic configuration management for Kubernetes.

- 🎹 **Synthesize**: generate manifests dynamically in short-lived pods
- ♻️ **Reconcile**: apply the generated configurations and rapidly correct any drift
- 🏃‍➡️ **React**: re-synthesize when input resources are modified

## What is Eno?

Eno deploys applications to Kubernetes using any programming language — not just YAML templates.

The Eno controllers execute your deployment code in short-lived pods and reconcile the results into Kubernetes resources.
Just print JSON objects to stdout and Eno will handle the rest.

## Docs

- [Synthesis](./docs/synthesis)
- [Reconciliation](./docs/reconciliation)
- [Symphony](./docs/symphony)
- [Synthesizer API](./docs/synthesizer-api.md)
- [Generated API Docs](./docs/api.md)

## Getting Started

### 1. Install Eno

```bash
export TAG=$(curl https://api.github.com/repos/Azure/eno/releases | jq -r '.[0].name')
kubectl apply -f "https://github.com/Azure/eno/releases/download/${TAG}/manifest.yaml"
```

### 2. Create a Synthesizer

Synthesizers model a reusable set of resources, similar to an `apt` package or Helm chart.

> ⚠️ This example uses a simple bash script but real applications should use [Helm](./examples/03-helm-shim), [Go](./examples/02-go-synthesizer/main.go), or [KCL](./pkg/kclshim/) (currently beta-ish) or any other process that implements the [KRM function API](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).

```yaml
kubectl apply -f - <<YAML
apiVersion: eno.azure.io/v1
kind: Synthesizer
metadata:
  name: getting-started
  namespace: default
spec:
  # Refs are like arguments.
  # They specify the type of an input required by the
  # synthesizer without "binding" to a particular resource.
  refs:
    - key: config
      resource:
        group: "" # core
        version: v1
        kind: ConfigMap

  # Synthesizers are simple containers distributed
  # as OCI images and executed in short-lived pods
  image: docker.io/ubuntu:latest
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

Compositions instantiate Synthesizers, similar to installing a package or creating a Helm release.

```yaml
kubectl apply -f - <<YAML
  apiVersion: eno.azure.io/v1
  kind: Composition
  metadata:
    name: my-first-composition
    namespace: default
  spec:
    synthesizer:
      name: getting-started # references the name of the Synthesizer object

    # Bindings assign a specific object to refs exposed by the Synthesizer.
    # Many compositions can use the one synthesizer while passing unique inputs.
    bindings:
      - key: config
        resource:
          name: my-first-config
          namespace: default

---

  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: my-first-config
    namespace: default
  data:
    replicas: "1"
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

Eno will automatically "resynthesize" the composition when its inputs change.

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
