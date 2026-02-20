package v1

import (
	corev1 "k8s.io/api/core/v1"
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
	Image string `json:"image,omitempty"`

	// Copied opaquely into the container's command property.
	//
	// +kubebuilder:default={"synthesize"}
	Command []string `json:"command,omitempty"`

	// DEPRECATED
	// Timeout for each execution of the synthesizer command.
	//
	// +kubebuilder:default="10s"
	ExecTimeout *metav1.Duration `json:"execTimeout,omitempty"`

	// DEPRECATED
	// Pods are recreated after they've existed for at least the pod timeout interval.
	// This helps close the loop in failure modes where a pod may be considered ready but not actually able to run.
	//
	// +kubebuilder:default="2m"
	PodTimeout *metav1.Duration `json:"podTimeout,omitempty"`

	// Refs define the Synthesizer's input schema without binding it to specific
	// resources.
	Refs []Ref `json:"refs,omitempty"`

	// PodOverrides sets values in the pods used to execute this synthesizer.
	PodOverrides PodOverrides `json:"podOverrides,omitempty"`
}

type PodOverrides struct {
	Labels      map[string]string           `json:"labels,omitempty"`
	Annotations map[string]string           `json:"annotations,omitempty"`
	Resources   corev1.ResourceRequirements `json:"resources,omitempty"`
	Affinity    *corev1.Affinity            `json:"affinity,omitempty"`
}

type SynthesizerStatus struct {
}

// +kubebuilder:validation:XValidation:rule="has(self.name) || has(self.labelSelector)",message="at least one of name or labelSelector must be set"
type SynthesizerRef struct {
	Name          string                `json:"name,omitempty"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

