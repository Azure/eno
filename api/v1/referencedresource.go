package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type ReferencedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReferencedResource `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type ReferencedResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReferencedResourceSpec   `json:"spec,omitempty"`
	Status ReferencedResourceStatus `json:"status,omitempty"`
}

type ReferencedResourceSpec struct {
	Input InputResource `json:"input,omitempty"`
}

type ReferencedResourceStatus struct {
	LastSeen *ReferencedResourceState `json:"lastSeen"`
}

type ReferencedResourceState struct {
	ObservationTime metav1.Time `json:"observationTime,omitempty"`
	Missing         bool        `json:"missing,omitempty"`
	ResourceVersion string      `json:"resourceVersion,omitempty"`
	AtomicVersion   int         `json:"atomicVersion,omitempty"`
}
