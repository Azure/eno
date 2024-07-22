# Getting Started

## Installing Eno

At the begining, install the Eno CRD resource defined at [here](https://github.com/Azure/eno/tree/main/api/v1/config/crd) to your cluster.

Eno consists of two deployments: the controller and the reconciler.
They are both typical controller-runtime based controllers and are installed with a static manifest.

- `eno-reconciler` manages the state of Kubernetes resources that are managed by Eno
- `eno-controller` handles the rest of the Eno functionality - spawning synthesizer pods, etc.

```bash
export TAG=$(curl https://api.github.com/repos/Azure/eno/releases | jq -r '.[0].name')
export REGISTRY="mcr.microsoft.com/aks/eno"
curl "https://raw.githubusercontent.com/Azure/eno/main/dev/deploy.yaml" | envsubst | kubectl apply -f -
```

## Hello World

Install the minimum viable Eno configuration to make sure everything works.
This manifest will create a configmap called "some-config" in the default namespace.

```bash
kubectl apply -f "https://raw.githubusercontent.com/Azure/eno/main/examples/minimal.yaml"
```

```bash
kubectl get cm some-config -o=yaml
```
