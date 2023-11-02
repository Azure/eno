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
	Generator         SynthesizerRef   `json:"generator,omitempty"`
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`
	Inputs            []InputRef       `json:"inputs,omitempty"`
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
	CurrentState  *GenerationAttempt `json:"currentState,omitempty"`
	PreviousState *GenerationAttempt `json:"previousState,omitempty"`
}

type GenerationAttempt struct {
	ObservedGeneration int64       `json:"observedGeneration,omitempty"`
	ResourceSliceCount int64       `json:"resourceSliceCount,omitempty"`
	Ready              bool        `json:"ready,omitempty"`
	Synced             bool        `json:"synced,omitempty"`
	PodCreation        metav1.Time `json:"podCreation,omitempty"`
}
