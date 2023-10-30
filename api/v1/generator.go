package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type GeneratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Generator `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type Generator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GeneratorSpec   `json:"spec,omitempty"`
	Status GeneratorStatus `json:"status,omitempty"`
}

type GeneratorSpec struct {
	// +required
	Image string `json:"image,omitempty"`
}

type GeneratorStatus struct {
	// The metadata.generation of this resource at the oldest version currently used by any Generations.
	// This will equal the current generation when slow rollout of an update to the Generations is complete.
	CurrentGeneration int64 `json:"currentGeneration,omitempty"`
}

type GeneratorRef struct {
	// +required
	Name string `json:"name,omitempty"`
}
