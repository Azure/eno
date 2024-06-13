package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type CompositionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Composition `json:"items"`
}

// Compositions represent a collection of related, synthesized resources.
//
// For example: when managing Postgres with Eno, one would create a composition
// per distinct instance of Postgres, all referencing a single synthesizer resource.
//
// Changing the spec of a composition will result in re-synthesis.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Synthesizer",type=string,JSONPath=`.spec.synthesizer.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.simplified.status`
// +kubebuilder:printcolumn:name="Error",type=string,JSONPath=`.status.simplified.error`
type Composition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CompositionSpec   `json:"spec,omitempty"`
	Status CompositionStatus `json:"status,omitempty"`
}

type CompositionSpec struct {
	// Compositions are synthesized by a Synthesizer, referenced by name.
	Synthesizer SynthesizerRef `json:"synthesizer,omitempty"`

	// Synthesizers can accept Kubernetes resources as inputs.
	// Bindings allow compositions to specify which resource to use for a particular input "reference".
	// Declaring extra bindings not (yet) supported by the synthesizer is valid.
	Bindings []Binding `json:"bindings,omitempty"`
}

type CompositionStatus struct {
	Simplified         *SimplifiedStatus `json:"simplified,omitempty"`
	PendingResynthesis *metav1.Time      `json:"pendingResynthesis,omitempty"`
	CurrentSynthesis   *Synthesis        `json:"currentSynthesis,omitempty"`
	PreviousSynthesis  *Synthesis        `json:"previousSynthesis,omitempty"`
}

type SimplifiedStatus struct {
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// A synthesis is the result of synthesizing a composition.
// In other words: it's a collection of resources returned from a synthesizer.
type Synthesis struct {
	// A random UUID scoped to this particular synthesis operation.
	// Used internally for strict ordering semantics.
	UUID string `json:"uuid,omitempty"`

	// The value of the composition's metadata.generation at the time the synthesis began.
	// This is a min i.e. a newer composition may have been used.
	ObservedCompositionGeneration int64 `json:"observedCompositionGeneration,omitempty"`

	// The value of the synthesizer's metadata.generation at the time the synthesis began.
	// This is a min i.e. a newer composition may have been used.
	ObservedSynthesizerGeneration int64 `json:"observedSynthesizerGeneration,omitempty"`

	// Time at which the most recent synthesizer pod was created.
	PodCreation *metav1.Time `json:"podCreation,omitempty"`

	// Time at which the synthesis completed i.e. resourceSlices was written
	Synthesized *metav1.Time `json:"synthesized,omitempty"`

	// Time at which the synthesis's resources were reconciled into real Kubernetes resources.
	Reconciled *metav1.Time `json:"reconciled,omitempty"`

	// Time at which the synthesis's reconciled resources became ready.
	Ready *metav1.Time `json:"ready,omitempty"`

	// Counter used internally to calculate back off when retrying failed syntheses.
	Attempts int `json:"attempts,omitempty"`

	// References to every resource slice that contains the resources comprising this synthesis.
	// Immutable.
	ResourceSlices []*ResourceSliceRef `json:"resourceSlices,omitempty"`

	// Results are passed through opaquely from the synthesizer's KRM function.
	Results []Result `json:"results,omitempty"`

	// InputRevisions contains the versions of the input resources that were used for this synthesis.
	InputRevisions []InputRevisions `json:"inputRevisions,omitempty"`
}

type Result struct {
	Message  string            `json:"message,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

type InputRevisions struct {
	Key             string `json:"key,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Revision        *int   `json:"revision,omitempty"`
}

func (s *Synthesis) Failed() bool {
	for _, result := range s.Results {
		if result.Severity == "error" {
			return true
		}
	}
	return false
}
