package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type GeneratedResourceSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GeneratedResourceSlice `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type GeneratedResourceSlice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GeneratedResourceSliceSpec   `json:"spec,omitempty"`
	Status GeneratedResourceSliceStatus `json:"status,omitempty"`
}

type GeneratedResourceSliceSpec struct {
	DerivedGeneration int64                `json:"derivedGeneration,omitempty"`
	Resources         []*GeneratedResource `json:"resources,omitempty"`
}

type GeneratedResource struct {
	Manifest string `json:"manifest,omitempty"`
}

type GeneratedResourceSliceStatus struct {
	Resources []*ResourceStatus `json:"resourceStatus,omitempty"`
}

type ResourceStatus struct {
	Ref        ResourceRef `json:"ref,omitempty"`
	Reconciled bool        `json:"reconciled,omitempty"`
	Ready      bool        `json:"ready,omitempty"`
	Message    string      `json:"message,omitempty"`
}
