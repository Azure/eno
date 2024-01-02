package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +kubebuilder:object:root=true
type ResourceSliceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceSlice `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type ResourceSlice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResourceSliceSpec   `json:"spec,omitempty"`
	Status ResourceSliceStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
type ResourceSliceSpec struct {
	Resources []Manifest `json:"resources,omitempty"`
}

type Manifest struct {
	// +required
	Manifest string `json:"manifest,omitempty"`

	// Deleted is true when this manifest represents a "tombstone" - a resource that should no longer exist.
	Deleted bool `json:"deleted,omitempty"`

	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`
}

type ResourceSliceStatus struct {
	// Elements of resources correspond in index to those in spec.resources at the observed generation.
	Resources []ResourceState `json:"resources,omitempty"`
}

type ResourceState struct {
	// True when the resource has been sync'd to the specified manifest.
	// This property latches: it will remain true if it has ever been true in the life of this resource.
	Reconciled bool `json:"reconciled,omitempty"`

	// nil if Eno is unable to determine the readiness of this resource.
	// Otherwise it is true when the resource is ready, false otherwise.
	// Like Reconciled, it latches and will never transition from true->false.
	Ready *bool `json:"ready,omitempty"`
}

type ResourceSliceRef struct {
	Name string `json:"name,omitempty"`
}
