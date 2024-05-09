package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type SynthesizerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Synthesizer `json:"items"`
}

// Synthesizers are any process that can run in a Kubernetes container that implements the [KRM Functions Specification](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).
//
// Synthesizer processes are given some metadata about the composition they are synthesizing, and are expected
// to return a set of Kubernetes resources. Essentially they generate the desired state for a set of Kubernetes resources.
//
// Eno honors a handful of annotations on resources emitted from synthesizers. They are consumed by Eno i.e. are not passed to the "real", reconciled resource.
// - eno.azure.io/reconcile-interval: How often to correct for any configuration drift. Accepts durations parsable by time.ParseDuration.
// - eno.azure.io/disable-updates: Ensure that the resource exists but never update it. Useful for populating resources you expect another user/process to mutate.
// - eno.azure.io/readiness: CEL expression used to assert that the resource is ready. More details below.
// - eno.azure.io/readiness-*: Same as above, allows for multiple readiness checks. All checks must pass for the resource to be considered ready.
//
// Readiness expressions can return either bool or a Kubernetes condition struct.
// If a condition is returned it will be used as the resource's readiness time, otherwise the controller will use wallclock time at the first moment it noticed the truthy value.
// When possible, match on a timestamp to preserve accuracy.
//
// Example matching on a condition:
// ```cel
//
//	self.status.conditions.filter(item, item.type == 'Test' && item.status == 'False')
//
// ```
//
// Example matching on a boolean:
// ```cel
//
//	self.status.foo == 'bar'
//
// ```
//
// A special resource can be returned from synthesizers: `eno.azure.io/v1.Patch`.
// Example:
//
// ```yaml
//
//	 # - Nothing will happen if the resource doesn't exist
//	 # - Patches are only applied when they would result in a change
//	 # - Deleting the Patch will not delete the referenced resource
//		apiVersion: eno.azure.io/v1
//		kind: Patch
//		metadata:
//			name: resource-to-be-patched
//			namespace: default
//		patch:
//			apiVersion: v1
//			kind: ConfigMap
//			ops: # standard jsonpatch operations
//			  - { "op": "add", "path": "/data/hello", "value": "world" }
//			  - { "op": "add", "path": "/metadata/deletionTimestamp", "value": "anything" } # setting any deletion timestamp will delete the resource
//
// ```
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
type Synthesizer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SynthesizerSpec   `json:"spec,omitempty"`
	Status SynthesizerStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="duration(self.execTimeout) <= duration(self.podTimeout)",message="podTimeout must be greater than execTimeout"
type SynthesizerSpec struct {
	// Copied opaquely into the container's image property.
	//
	// +required
	Image string `json:"image,omitempty"`

	// Copied opaquely into the container's command property.
	//
	// +kubebuilder:default={"synthesize"}
	Command []string `json:"command,omitempty"`

	// Timeout for each execution of the synthesizer command.
	//
	// +kubebuilder:default="10s"
	ExecTimeout *metav1.Duration `json:"execTimeout,omitempty"`

	// Pods are recreated after they've existed for at least the pod timeout interval.
	// This helps close the loop in failure modes where a pod may be considered ready but not actually able to run.
	//
	// +kubebuilder:default="2m"
	PodTimeout *metav1.Duration `json:"podTimeout,omitempty"`

	// Synthesized resources can optionally be reconciled at a given interval.
	// Per-resource jitter will be applied to avoid spikes in request rate.
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`

	// Refs define the Synthesizer's input schema without binding it to specific
	// resources.
	Refs []Ref `json:"refs,omitempty"`
}

func (s *Synthesizer) GetRef(key string) *Ref {
	for _, ref := range s.Spec.Refs {
		if ref.Key == key {
			return &ref
		}
	}
	return nil
}


type SynthesizerStatus struct {
}

type SynthesizerRef struct {
	// +required
	Name string `json:"name,omitempty"`
}
