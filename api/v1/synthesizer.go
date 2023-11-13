package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type SynthesizerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Synthesizer `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type Synthesizer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SynthesizerSpec   `json:"spec,omitempty"`
	Status SynthesizerStatus `json:"status,omitempty"`
}

type SynthesizerSpec struct {
	// +required
	Image string `json:"image,omitempty"`
}

type SynthesizerStatus struct {
	// LastRolloutTime is the timestamp of the last pod creation caused by a change to this resource.
	// Should not be updated due to Composotion changes.
	// Used to calculate rollout cooldown period.
	LastRolloutTime *metav1.Time `json:"lastRolloutTime,omitempty"`
}

type SynthesizerRef struct {
	// +required
	Name string `json:"name,omitempty"`
}
