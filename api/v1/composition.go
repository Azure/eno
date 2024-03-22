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
}

type CompositionStatus struct {
	CurrentSynthesis  *Synthesis `json:"currentSynthesis,omitempty"`
	PreviousSynthesis *Synthesis `json:"previousSynthesis,omitempty"`
}

// Synthesis represents a Synthesizer's specific synthesis of a given Composition.
type Synthesis struct {
	UUID                          string `json:"uuid,omitempty"`
	ObservedCompositionGeneration int64  `json:"observedCompositionGeneration,omitempty"`
	ObservedSynthesizerGeneration int64  `json:"observedSynthesizerGeneration,omitempty"`

	Started     *metav1.Time `json:"started,omitempty"`
	PodCreation *metav1.Time `json:"podCreation,omitempty"`
	Synthesized *metav1.Time `json:"synthesized,omitempty"`
	Reconciled  *metav1.Time `json:"reconciled,omitempty"`
	Ready       *metav1.Time `json:"ready,omitempty"`

	ResourceSlices []*ResourceSliceRef `json:"resourceSlices,omitempty"`
}
