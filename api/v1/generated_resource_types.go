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
	Resources []*ReconciliationStatus `json:"reconciliation,omitempty"`
}

type ReconciliationStatus struct {
	Ref ResourceRef `json:"ref,omitempty"`

	// Reconciled is true when the resource has been reconciled with the current manifest.
	Reconciled bool `json:"reconciled,omitempty"`

	// Ready is true when the resource has been initialized based on its own semantics.
	// For example, deployments are ready when the expected number of up-to-date pod are passing readiness probes.
	Ready bool `json:"ready,omitempty"`

	// Message is a human-readable opaque string that describes the current state.
	Message string `json:"message,omitempty"`
}

type ResourceRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}
