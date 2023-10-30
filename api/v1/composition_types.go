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
	Revision   int64         `json:"revision,omitempty"`
	Generator  *GeneratorRef `json:"generator,omitempty"`
	Inputs     []InputRef    `json:"inputs,omitempty"`
	KubeConfig *SecretKeyRef `json:"kubeConfig,omitempty"`
}

const (
	ReconciledConditionType = "eno.azure.io/reconciled"
	ReadyConditionType      = "eno.azure.io/ready"
)

type CompositionStatus struct {
	CompositionGeneration int64 `json:"compositionGeneration,omitempty"`
	GeneratorGeneration   int64 `json:"generatorGeneration,omitempty"`

	LastGeneratorCreation  *metav1.Time       `json:"lastGeneratorCreation,omitempty"`
	GeneratedResourceCount int64              `json:"generatedResourceCount,omitempty"`
	Conditions             []metav1.Condition `json:"conditions,omitempty"`
}
