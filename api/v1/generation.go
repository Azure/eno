package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type GenerationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Generation `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Generation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GenerationSpec   `json:"spec,omitempty"`
	Status GenerationStatus `json:"status,omitempty"`
}

type GenerationSpec struct {
	Generator GeneratorRef `json:"generator,omitempty"`
	Inputs    []InputRef   `json:"inputs,omitempty"`
}

type InputRef struct {
	// +required
	Name string `json:"name,omitempty"`

	Resource *ResourceInputRef `json:"resource,omitempty"`
}

type ResourceInputRef struct {
	// +required
	APIVersion string `json:"apiVersion,omitempty"`
	// +required
	Kind string `json:"kind,omitempty"`
	// +required
	Namespace string `json:"namespace,omitempty"`
	// +required
	Name string `json:"name,omitempty"`
}

type GenerationStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	Ready              bool  `json:"ready,omitempty"`
	Synced             bool  `json:"synced,omitempty"`

	ActiveSlices   []*GeneratedResourceSliceRef `json:"activeResources,omitempty"`
	LastGeneration *GenerationAttempt           `json:"lastGeneration,omitempty"`
}

type GenerationAttempt struct {
	ObservedGeneration int64       `json:"observedGeneration,omitempty"`
	PodCreation        metav1.Time `json:"podCreation,omitempty"`
}
