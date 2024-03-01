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

// +kubebuilder:validation:XValidation:rule="duration(self.execTimeout) <= duration(self.podTimeout)",message="podTimeout must be greater than execTimeout"
type SynthesizerSpec struct {
	// +required
	Image string `json:"image,omitempty"`

	// +kubebuilder:default={"synthesize"}
	Command []string `json:"command,omitempty"`

	// Timeout for each execution of the synthesizer command.
	//
	// +kubebuilder:default="10s"
	ExecTimeout metav1.Duration `json:"execTimeout,omitempty"`

	// Pods are recreated after they've existed for at least the pod timeout interval.
	// This helps close the loop in failure modes where a pod may be considered ready but not actually able to run.
	//
	// +kubebuilder:default="2m"
	PodTimeout metav1.Duration `json:"podTimeout,omitempty"`

	// Any changes to the synthesizer will be propagated to compositions that reference it.
	// This property controls how long Eno will wait between each composition update.
	//
	// +kubebuilder:default="30s"
	RolloutCooldown metav1.Duration `json:"rolloutCooldown,omitempty"`
}

type SynthesizerStatus struct {
	// The metadata.generation of this resource at the oldest version currently used by any Generations.
	// This will equal the current generation when slow rollout of an update to the Generations is complete.
	CurrentGeneration int64 `json:"currentGeneration,omitempty"`

	// LastRolloutTime is the timestamp of the last pod creation caused by a change to this resource.
	// Should not be updated due to Composotion changes.
	// Used to calculate rollout cooldown period.
	LastRolloutTime *metav1.Time `json:"lastRolloutTime,omitempty"`
}

type SynthesizerRef struct {
	// +required
	Name string `json:"name,omitempty"`

	// Compositions will be resynthesized if their status.currentState.observedSynthesizerGeneration is < the referenced synthesizer's generation.
	// Used to slowly roll out synthesizer updates across compositions.
	MinGeneration int64 `json:"minGeneration,omitempty"`
}
