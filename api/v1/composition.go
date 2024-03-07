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
	// Compositions are synthesized by a Synthesizer.
	Synthesizer SynthesizerRef `json:"synthesizer,omitempty"`

	// Synthesized resources can optionally be reconciled at a given interval.
	// Per-resource jitter will be applied to avoid spikes in request rate.
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`

	// Inputs are string literals provided to the Synthesizer during synthesis.
	Inputs []Input `json:"inputs,omitempty"`
}

type Input struct {
	// +required
	Name string `json:"name,omitempty"`

	Value string `json:"value,omitempty"`
}

type CompositionStatus struct {
	CurrentState  *Synthesis `json:"currentState,omitempty"`
	PreviousState *Synthesis `json:"previousState,omitempty"`
}

// Synthesis represents a Synthesizer's specific synthesis of a given Composition.
type Synthesis struct {
	ObservedCompositionGeneration int64 `json:"observedCompositionGeneration,omitempty"`
	ObservedSynthesizerGeneration int64 `json:"observedSynthesizerGeneration,omitempty"`

	PodCreation    *metav1.Time        `json:"podCreation,omitempty"`
	ResourceSlices []*ResourceSliceRef `json:"resourceSlices,omitempty"`

	Synthesized bool `json:"synthesized,omitempty"`
	Ready       bool `json:"ready,omitempty"`
	Synced      bool `json:"synced,omitempty"`
}
