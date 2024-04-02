package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type SymphonyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Symphony `json:"items"`
}

// Symphony is a set of variations on a composition.
// Useful for creating several compositions that use a common set of bindings but different synthesizers.
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
	Variations []Variation `json:"variations,omitempty"`
	Bindings   []Binding   `json:"bindings,omitempty"`
}

type SymphonyStatus struct {
	Synthesized  *metav1.Time     `json:"synthesized,omitempty"`
	Reconciled   *metav1.Time     `json:"reconciled,omitempty"`
	Ready        *metav1.Time     `json:"ready,omitempty"`
	Synthesizers []SynthesizerRef `json:"synthesizers,omitempty"`
}

type Variation struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Synthesizer SynthesizerRef    `json:"synthesizer,omitempty"`
}
