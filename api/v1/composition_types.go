package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type CompositionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Composition `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Composition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CompositionSpec   `json:"spec,omitempty"`
	Status CompositionStatus `json:"status,omitempty"`
}

type CompositionSpec struct {
	Generator *GeneratorRef `json:"generator,omitempty"`
	Inputs    []InputRef    `json:"inputs,omitempty"`
}

type CompositionStatus struct {
	ObservedGeneration int64            `json:"observedGeneration,omitempty"`
	InSync             bool             `json:"inSync,omitempty"`
	NonReadyResources  []ResourceStatus `json:"nonReadyResources,omitempty"` // limited to ~50, just to find deadlocks

	GeneratorGeneration   int64        `json:"generatorGeneration,omitempty"`
	LastGeneratorCreation *metav1.Time `json:"lastGeneratorCreation,omitempty"`
}
