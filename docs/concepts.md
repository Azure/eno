# Concepts


## Synthesis

- Synthesis is the core concept of Eno
- During synthesis, a `Composition` is given to a `Synthesizer`, which produces a set of Kubernetes resources based on any input resources bound to the `Composition`
- Synthesizers can be written in any language capable of implementing the [KRM Function](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md) specification

### Inputs

- Synthesizers can expose typed "refs" to resources they require
- Refs are "bound" to specific resources by compositions
- In this way, Eno uses Kubernetes' type system for synthesizer inputs

### Outputs

- Resources returned by synthesizers are "reconciled" into real Kubernetes resources
- Omitting a resource that was once returned by the synthesizer will cause it to be deleted, updating it will cause it to be patched, etc.
- Each resource is handled as an individual unit owned (logically, not in terms of `ownerReferences`) by its composition
  - Resource reconciliation is handled by a standard Kubernetes work queue where the key is a unique identifier of that resource
  - So retries, flow control, etc. are scoped to individual resources


## Scoping

- Synthesizers are cluster-scoped and can be referenced by many compositions
  - They represent reusable configurations, similar to Helm charts
- Compositions have a set of synthesized resources
  - These resources may or may not be in the same namespace as their composition
- Symphonies have a set of compositions
  - They represent a logical unit of configuration that spans multiple synthesizers


## Feedback

- Eno allows synthesizers to use controller-like semantics without having their own long-running pods, informers, etc.
- Synthesis will be triggered for a given composition when the synthesizer or any bound resources have changed
- These updates are subject to a global cooldown period and various other flow-control techniques
