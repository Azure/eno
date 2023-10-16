package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type GeneratedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GeneratedResource `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type GeneratedResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GeneratedResourceSpec   `json:"spec,omitempty"`
	Status GeneratedResourceStatus `json:"status,omitempty"`
}

type GeneratedResourceSpec struct {
	Manifest          string           `json:"manifest,omitempty"`
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`
	DerivedGeneration int64            `json:"derivedGeneration,omitempty"`
	KubeConfig        *SecretKeyRef    `json:"kubeConfig,omitempty"`
}

type GeneratedResourceStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
