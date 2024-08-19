# Eno

## What is Eno?

Eno is a configuration management tool for Kubernetes.

- Generate configurations using short-lived pods, language-agnostic
- Dynamically regenerate when input resources change (without writing a controller!)
- Safely roll out configuration changes
- Define complex ordering relationships
- Control many low-trust clusters from a single management cluster
- Support high object cardinality (10s of thousands)

## Getting Started

Install the Eno CRD resource defined at [here](https://github.com/Azure/eno/tree/main/api/v1/config/crd) to your cluster.

Eno consists of two deployments: the controller and the reconciler.

- `eno-reconciler` reconciles Kubernetes resources into the expected state
- `eno-controller` spawns pods to generate expected resource states

```bash
export TAG=$(curl https://api.github.com/repos/Azure/eno/releases | jq -r '.[0].name')
export REGISTRY="mcr.microsoft.com/aks/eno"
curl "https://raw.githubusercontent.com/Azure/eno/main/dev/deploy.yaml" | envsubst | kubectl apply -f -
```

Install the minimum viable Eno configuration to make sure everything works.
This manifest will create a configmap called "some-config" in the default namespace.

```bash
kubectl apply -f "https://raw.githubusercontent.com/Azure/eno/main/examples/minimal.yaml"

# The configmap should be created by Eno soon after
kubectl get cm some-config -o=yaml
```

## Docs

- [Reference](./docs/reference.md)
- [API](./docs/api.md)

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
