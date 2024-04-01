package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type SymphonyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Symphony `json:"items"`
}

// Symphony represents a "meta-composition" that spawns a set of child compositions
// for each in a set of synthesizers.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type Symphony struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SymphonySpec   `json:"spec,omitempty"`
	Status SymphonyStatus `json:"status,omitempty"`
}

type SymphonySpec struct {
	Synthesizers []SynthesizerRef `json:"synthesizers,omitempty"`
	Bindings     []Binding        `json:"bindings,omitempty"`
}

type SymphonyStatus struct {
	Synthesized  *metav1.Time     `json:"synthesized,omitempty"`
	Reconciled   *metav1.Time     `json:"reconciled,omitempty"`
	Ready        *metav1.Time     `json:"ready,omitempty"`
	Synthesizers []SynthesizerRef `json:"synthesizers,omitempty"`
}
