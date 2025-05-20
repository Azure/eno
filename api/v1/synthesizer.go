package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type SynthesizerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Synthesizer `json:"items"`
}

// Synthesizers are any process that can run in a Kubernetes container that implements the [KRM Functions Specification](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).
//
// Synthesizer processes are given some metadata about the composition they are synthesizing, and are expected
// to return a set of Kubernetes resources. Essentially they generate the desired state for a set of Kubernetes resources.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
type Synthesizer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SynthesizerSpec   `json:"spec,omitempty"`
	Status SynthesizerStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="duration(self.execTimeout) <= duration(self.podTimeout)",message="podTimeout must be greater than execTimeout"
type SynthesizerSpec struct {
	// Copied opaquely into the container's image property.
	Image string `json:"image,omitempty"`

	// Copied opaquely into the container's command property.
	//
	// +kubebuilder:default={"synthesize"}
	Command []string `json:"command,omitempty"`

	// Timeout for each execution of the synthesizer command.
	//
	// +kubebuilder:default="10s"
	ExecTimeout *metav1.Duration `json:"execTimeout,omitempty"`

	// Pods are recreated after they've existed for at least the pod timeout interval.
	// This helps close the loop in failure modes where a pod may be considered ready but not actually able to run.
	//
	// +kubebuilder:default="2m"
	PodTimeout *metav1.Duration `json:"podTimeout,omitempty"`

	// Paused indicates that rollout of changes to compositions using this synthesizer should be suspended.
	// Changes to the synthesizer will not be applied until Paused is set to false.
	Paused bool `json:"paused,omitempty"`

	// Refs define the Synthesizer's input schema without binding it to specific
	// resources.
	Refs []Ref `json:"refs,omitempty"`

	// PodOverrides sets values in the pods used to execute this synthesizer.
	PodOverrides PodOverrides `json:"podOverrides,omitempty"`
}

type PodOverrides struct {
	Labels      map[string]string           `json:"labels,omitempty"`
	Annotations map[string]string           `json:"annotations,omitempty"`
	Resources   corev1.ResourceRequirements `json:"resources,omitempty"`
	Affinity    *corev1.Affinity            `json:"affinity,omitempty"`
}

// SynthesizerStatus contains information about the current state of the synthesizer
type SynthesizerStatus struct {
	// Conditions provide information about the current state of the synthesizer rollout
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SynthesizerConditionType is a valid value for Condition.Type for the Synthesizer resource
type SynthesizerConditionType string

const (
	// RolloutCompletedCondition indicates whether all compositions using this synthesizer
	// have been updated to the current generation
	RolloutCompletedCondition SynthesizerConditionType = "RolloutCompleted"

	// FirstSuccessfulReconciliationCondition indicates whether at least one composition
	// has successfully reconciled with the current synthesizer generation
	FirstSuccessfulReconciliationCondition SynthesizerConditionType = "FirstSuccessfulReconciliation"
)

type SynthesizerRef struct {
	Name string `json:"name,omitempty"`
}

// SetCondition updates or adds the provided condition to the synthesizer status
func (s *Synthesizer) SetCondition(cType SynthesizerConditionType, status metav1.ConditionStatus, reason, message string) {
	if s.Status.Conditions == nil {
		s.Status.Conditions = []metav1.Condition{}
	}

	now := metav1.Now()
	existingCondition := s.GetCondition(cType)

	if existingCondition == nil {
		s.Status.Conditions = append(s.Status.Conditions, metav1.Condition{
			Type:               string(cType),
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: now,
		})
		return
	}

	// Only update if values have changed
	if existingCondition.Status != status || existingCondition.Reason != reason || existingCondition.Message != message {
		// Replace the entire condition instead of mutating it
		for i := range s.Status.Conditions {
			if s.Status.Conditions[i].Type == string(cType) {
				lastTransitionTime := existingCondition.LastTransitionTime
				if existingCondition.Status != status {
					lastTransitionTime = now
				}

				s.Status.Conditions[i] = metav1.Condition{
					Type:               string(cType),
					Status:             status,
					Reason:             reason,
					Message:            message,
					LastTransitionTime: lastTransitionTime,
				}
				break
			}
		}
	}
}

// GetCondition returns the condition with the given type from the synthesizer status
func (s *Synthesizer) GetCondition(cType SynthesizerConditionType) *metav1.Condition {
	for i := range s.Status.Conditions {
		if s.Status.Conditions[i].Type == string(cType) {
			return &s.Status.Conditions[i]
		}
	}
	return nil
}
