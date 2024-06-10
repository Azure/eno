# Concepts

## Synthesis

Synthesis is the core concept of Eno.
During synthesis, a `Composition` is given to a `Synthesizer`, which produces a set of Kubernetes resources based on any input resources bound to the `Composition`.

Synthesizers can be written in any language capable of implementing the [KRM Function](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md) specification.

### Inputs

Synthesizers can expose typed "refs" to resources they require, which are then "bound" to specific resources by compositions.
In this way, Eno uses Kubernetes' type system for synthesizer inputs.

### Outputs

Resources returned by synthesizers are "reconciled" into real Kubernetes resources. Omitting a resource that was once returned by the synthesizer will cause it to be deleted, updating it will cause it to be patched, etc.

Each resource is handled as an individual unit within Eno, although they are "owned" in some sense by a single composition. Their reconciliation is handled by a standard Kubernetes work queue where the key is a unique identifier of that resource. So retries, flow control, etc. are scoped to individual resources.

Eno will use strategic merge patches when possible to avoid stomping on properties written by other controllers.
If not supported by a resource it will fall back to normal three-way merge.



## Re-synthesis

Synthesis will be triggered for a given composition whenever the synthesizer or any bound resources have changed.

Re-synthesis is a compromise: it allows dynamic, controller-style runtime behavior without requiring synthesizer pods to run constantly.
So while it's possible to change configurations based on the status of a resource, beware that every change will result in spawning the synthesizer in a new pod.

### Change Propagation

Synthesizer and any non-exempt input changes are subject to a global "cooldown period" configured on the Eno controller.
The idea is to slowly roll changes that apply to many compositions, to provide adequate time to catch any issues.
Beware that rollout will continue regardless of success rate.
So automatic rollback is possible by "feeding back" status into the synthesizer, but not built in.



## Namespacing

Synthesizers a cluster-scoped resources, since they are reusable units of configuration.
Compositions are namespaced, although they can contain resources in any namespace.

Conceptually, Eno is a cluster-scoped component.
Compositions are only namespaced for the sake of organization and partitioning,
which is useful in cases where a single cluster has thousands of compositions.
