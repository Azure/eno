# API Reference

## Packages
- [eno.azure.io/v1](#enoazureiov1)


## eno.azure.io/v1


### Resource Types
- [Composition](#composition)
- [Symphony](#symphony)
- [Synthesizer](#synthesizer)



#### Binding



Bindings map a specific Kubernetes resource to a ref exposed by a synthesizer.
Compositions use bindings to populate inputs supported by their synthesizer.



_Appears in:_
- [CompositionSpec](#compositionspec)
- [SymphonySpec](#symphonyspec)
- [Variation](#variation)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `key` _string_ | Key determines which ref this binding binds to. Opaque. |  |  |
| `resource` _[ResourceBinding](#resourcebinding)_ |  |  |  |


#### Composition



Compositions represent a collection of related, synthesized resources.


For example: when managing Postgres with Eno, one would create a composition
per distinct instance of Postgres, all referencing a single synthesizer resource.


Changing the spec of a composition will result in re-synthesis.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `eno.azure.io/v1` | | |
| `kind` _string_ | `Composition` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[CompositionSpec](#compositionspec)_ |  |  |  |
| `status` _[CompositionStatus](#compositionstatus)_ |  |  |  |


#### CompositionSpec







_Appears in:_
- [Composition](#composition)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `synthesizer` _[SynthesizerRef](#synthesizerref)_ | Compositions are synthesized by a Synthesizer, referenced by name. |  |  |
| `bindings` _[Binding](#binding) array_ | Synthesizers can accept Kubernetes resources as inputs.<br />Bindings allow compositions to specify which resource to use for a particular input "reference".<br />Declaring extra bindings not (yet) supported by the synthesizer is valid. |  |  |
| `synthesisEnv` _[EnvVar](#envvar) array_ | SynthesisEnv<br />A set of environment variables that will be made available inside the synthesis Pod. |  | MaxItems: 500 <br /> |


#### CompositionStatus







_Appears in:_
- [Composition](#composition)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `simplified` _[SimplifiedStatus](#simplifiedstatus)_ |  |  |  |
| `currentSynthesis` _[Synthesis](#synthesis)_ |  |  |  |
| `previousSynthesis` _[Synthesis](#synthesis)_ |  |  |  |
| `pendingResynthesis` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ |  |  |  |
| `inputRevisions` _[InputRevisions](#inputrevisions) array_ |  |  |  |


#### EnvVar







_Appears in:_
- [CompositionSpec](#compositionspec)
- [SymphonySpec](#symphonyspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ |  |  | MaxLength: 1000 <br /> |
| `value` _string_ |  |  |  |




#### InputRevisions







_Appears in:_
- [CompositionStatus](#compositionstatus)
- [Synthesis](#synthesis)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `key` _string_ |  |  |  |
| `resourceVersion` _string_ |  |  |  |
| `revision` _integer_ |  |  |  |
| `synthesizerGeneration` _integer_ |  |  |  |




#### PodOverrides







_Appears in:_
- [SynthesizerSpec](#synthesizerspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labels` _object (keys:string, values:string)_ |  |  |  |
| `annotations` _object (keys:string, values:string)_ |  |  |  |
| `resources` _[ResourceRequirements](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#resourcerequirements-v1-core)_ |  |  |  |


#### Ref



Ref defines a synthesizer input.
Inputs are typed using the Kubernetes API - they are just normal Kubernetes resources.
The consumer (synthesizer) specifies the resource's kind/group,
while the producer (composition) specifies a specific resource name/namespace.


Compositions that use the synthesizer will be re-synthesized when the resource bound to this ref changes.
Re-synthesis happens automatically while honoring the globally configured cooldown period.



_Appears in:_
- [SynthesizerSpec](#synthesizerspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `key` _string_ | Key corresponds to bindings to this ref. |  |  |
| `resource` _[ResourceRef](#resourceref)_ |  |  |  |
| `defer` _boolean_ | Allows control over re-synthesis when inputs changed.<br />A non-deferred input will trigger a synthesis immediately, whereas a<br />deferred input will respect the cooldown period. |  |  |


#### ResourceBinding



A reference to a specific resource name and optionally namespace.



_Appears in:_
- [Binding](#binding)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ |  |  |  |
| `namespace` _string_ |  |  |  |


#### ResourceRef



A reference to a resource kind/group.



_Appears in:_
- [Ref](#ref)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `group` _string_ |  |  |  |
| `version` _string_ |  |  |  |
| `kind` _string_ |  |  |  |




#### Result







_Appears in:_
- [Synthesis](#synthesis)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `message` _string_ |  |  |  |
| `severity` _string_ |  |  |  |
| `tags` _object (keys:string, values:string)_ |  |  |  |


#### SimplifiedStatus







_Appears in:_
- [CompositionStatus](#compositionstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `status` _string_ |  |  |  |
| `error` _string_ |  |  |  |


#### Symphony



Symphony is a set of variations on a composition.
Useful for creating several compositions that use a common set of bindings but different synthesizers.


This pattern is highly opinionated for use-cases in which a single "unit of management"
includes multiple distinct components. For example: deploying many instances of an application that
is comprised of several components (Wordpress, etc.).





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `eno.azure.io/v1` | | |
| `kind` _string_ | `Symphony` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[SymphonySpec](#symphonyspec)_ |  |  |  |
| `status` _[SymphonyStatus](#symphonystatus)_ |  |  |  |


#### SymphonySpec







_Appears in:_
- [Symphony](#symphony)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `variations` _[Variation](#variation) array_ | Each variation will result in the creation of a composition.<br />Synthesizer refs must be unique across variations.<br />Removing a variation will cause the composition to be deleted! |  |  |
| `bindings` _[Binding](#binding) array_ | Bindings are inherited by all compositions managed by this symphony. |  |  |
| `synthesisEnv` _[EnvVar](#envvar) array_ | SynthesisEnv<br />Copied opaquely into the compositions managed by this symphony. |  | MaxItems: 500 <br /> |


#### SymphonyStatus







_Appears in:_
- [Symphony](#symphony)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `observedGeneration` _integer_ |  |  |  |
| `synthesized` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ |  |  |  |
| `reconciled` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ |  |  |  |
| `ready` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ |  |  |  |
| `synthesizers` _[SynthesizerRef](#synthesizerref) array_ |  |  |  |


#### Synthesis



A synthesis is the result of synthesizing a composition.
In other words: it's a collection of resources returned from a synthesizer.



_Appears in:_
- [CompositionStatus](#compositionstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `uuid` _string_ | A random UUID scoped to this particular synthesis operation.<br />Used internally for strict ordering semantics. |  |  |
| `observedCompositionGeneration` _integer_ | The value of the composition's metadata.generation at the time the synthesis began.<br />This is a min i.e. a newer composition may have been used. |  |  |
| `observedSynthesizerGeneration` _integer_ | The value of the synthesizer's metadata.generation at the time the synthesis began.<br />This is a min i.e. a newer composition may have been used. |  |  |
| `podCreation` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ | Time at which the most recent synthesizer pod was created. |  |  |
| `synthesized` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ | Time at which the synthesis completed i.e. resourceSlices was written |  |  |
| `reconciled` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ | Time at which the synthesis's resources were reconciled into real Kubernetes resources. |  |  |
| `ready` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#time-v1-meta)_ | Time at which the synthesis's reconciled resources became ready. |  |  |
| `attempts` _integer_ | Counter used internally to calculate back off when retrying failed syntheses. |  |  |
| `results` _[Result](#result) array_ | Results are passed through opaquely from the synthesizer's KRM function. |  |  |
| `inputRevisions` _[InputRevisions](#inputrevisions) array_ | InputRevisions contains the versions of the input resources that were used for this synthesis. |  |  |
| `deferred` _boolean_ | Deferred is true when this synthesis was caused by a change to either the synthesizer<br />or an input with a ref that sets `Defer == true`. |  |  |


#### Synthesizer



Synthesizers are any process that can run in a Kubernetes container that implements the [KRM Functions Specification](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).


Synthesizer processes are given some metadata about the composition they are synthesizing, and are expected
to return a set of Kubernetes resources. Essentially they generate the desired state for a set of Kubernetes resources.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `eno.azure.io/v1` | | |
| `kind` _string_ | `Synthesizer` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[SynthesizerSpec](#synthesizerspec)_ |  |  |  |
| `status` _[SynthesizerStatus](#synthesizerstatus)_ |  |  |  |


#### SynthesizerRef







_Appears in:_
- [CompositionSpec](#compositionspec)
- [SymphonyStatus](#symphonystatus)
- [Variation](#variation)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ |  |  |  |


#### SynthesizerSpec







_Appears in:_
- [Synthesizer](#synthesizer)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `image` _string_ | Copied opaquely into the container's image property. |  |  |
| `command` _string array_ | Copied opaquely into the container's command property. | [synthesize] |  |
| `execTimeout` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#duration-v1-meta)_ | Timeout for each execution of the synthesizer command. | 10s |  |
| `podTimeout` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#duration-v1-meta)_ | Pods are recreated after they've existed for at least the pod timeout interval.<br />This helps close the loop in failure modes where a pod may be considered ready but not actually able to run. | 2m |  |
| `reconcileInterval` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.22/#duration-v1-meta)_ | Synthesized resources can optionally be reconciled at a given interval.<br />Per-resource jitter will be applied to avoid spikes in request rate. |  |  |
| `refs` _[Ref](#ref) array_ | Refs define the Synthesizer's input schema without binding it to specific<br />resources. |  |  |
| `podOverrides` _[PodOverrides](#podoverrides)_ | PodOverrides sets values in the pods used to execute this synthesizer. |  |  |


#### SynthesizerStatus







_Appears in:_
- [Synthesizer](#synthesizer)



#### Variation







_Appears in:_
- [SymphonySpec](#symphonyspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `labels` _object (keys:string, values:string)_ | Used to populate the composition's metadata.labels. |  |  |
| `annotations` _object (keys:string, values:string)_ | Used to populate the composition's medatada.annotations. |  |  |
| `synthesizer` _[SynthesizerRef](#synthesizerref)_ | Used to populate the composition's spec.synthesizer. |  |  |
| `bindings` _[Binding](#binding) array_ | Variation-specific bindings get merged with Symphony bindings and take<br />precedence over them. |  |  |


