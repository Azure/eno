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
	Synthesizer       SynthesizerRef   `json:"synthesizer,omitempty"`
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

type CompositionStatus struct {
	CurrentState  *Synthesis `json:"currentState,omitempty"`
	PreviousState *Synthesis `json:"previousState,omitempty"`
}

type Synthesis struct {
	ObservedGeneration int64       `json:"observedGeneration,omitempty"`
	ResourceSliceCount int64       `json:"resourceSliceCount,omitempty"`
	Ready              bool        `json:"ready,omitempty"`
	Synced             bool        `json:"synced,omitempty"`
	PodCreation        metav1.Time `json:"podCreation,omitempty"`
}
