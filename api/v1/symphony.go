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

	// Bindings are inherited by all compositions managed by this symphony.
	Bindings []Binding `json:"bindings,omitempty"`

	// SynthesisEnv
	// Copied opaquely into the compositions managed by this symphony.
	// +kubebuilder:validation:MaxItems:=500
	SynthesisEnv []EnvVar `json:"synthesisEnv,omitempty"`
}

type SymphonyStatus struct {
	ObservedGeneration int64            `json:"observedGeneration,omitempty"`
	Synthesized        *metav1.Time     `json:"synthesized,omitempty"`
	Reconciled         *metav1.Time     `json:"reconciled,omitempty"`
	Ready              *metav1.Time     `json:"ready,omitempty"`
	Synthesizers       []SynthesizerRef `json:"synthesizers,omitempty"`
}

type Variation struct {
	// Used to populate the composition's metadata.labels.
	Labels map[string]string `json:"labels,omitempty"`

	// Used to populate the composition's medatada.annotations.
	Annotations map[string]string `json:"annotations,omitempty"`

	// Used to populate the composition's spec.synthesizer.
	Synthesizer SynthesizerRef `json:"synthesizer,omitempty"`

	// Variation-specific bindings get merged with Symphony bindings and take
	// precedence over them.
	Bindings []Binding `json:"bindings,omitempty"`
}
