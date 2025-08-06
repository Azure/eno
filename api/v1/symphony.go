package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type SymphonyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Symphony `json:"items"`
}

// Symphony is a set of variations on a composition.
//
// This pattern is highly opinionated for use-cases in which a single "unit of management"
// includes multiple distinct components. For example: deploying many instances of an application that
// is comprised of several components (Wordpress, etc.).
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
	// Each variation will result in the creation of a composition.
	// Synthesizer refs must be unique across variations.
	// Removing a variation will cause the composition to be deleted!
	Variations []Variation `json:"variations,omitempty"`
}

type SymphonyStatus struct {
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	Synthesized        *metav1.Time `json:"synthesized,omitempty"`
	Reconciled         *metav1.Time `json:"reconciled,omitempty"`
	Ready              *metav1.Time `json:"ready,omitempty"`
}

type Variation struct {
	Labels       map[string]string `json:"labels,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Synthesizer  SynthesizerRef    `json:"synthesizer,omitempty"`
	Bindings     []Binding         `json:"bindings,omitempty"`
	SynthesisEnv []EnvVar          `json:"synthesisEnv,omitempty"`
}
