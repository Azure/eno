package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type CompositionSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Composition `json:"items"`
}

// CompositionSet represents a "meta-composition" that spawns a set of child compositions
// for each in a set of synthesizers.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type CompositionSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CompositionSetSpec   `json:"spec,omitempty"`
	Status CompositionSetStatus `json:"status,omitempty"`
}

type CompositionSetSpec struct {
	Synthesizers []SynthesizerRef `json:"synthesizers,omitempty"`
	Bindings     []Binding        `json:"bindings,omitempty"`
}

type CompositionSetStatus struct {
	Synthesized *metav1.Time `json:"synthesized,omitempty"`
	Reconciled  *metav1.Time `json:"reconciled,omitempty"`
	Ready       *metav1.Time `json:"ready,omitempty"`
}
