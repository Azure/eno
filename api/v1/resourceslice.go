package v1

// TODO: Set correct plural name

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
	CompositionGeneration int64 `json:"compositionGeneration,omitempty"`

	Resources []ResourceSpec `json:"resources,omitempty"`
}

type ResourceSliceStatus struct {
	// Elements of resources correspond in index to those in spec.resources at the observed generation.
	Resources []ResourceStatus `json:"resources,omitempty"`
}

// TODO: Consider renaming to Manifest?
type ResourceSpec struct {
	// +required
	Manifest string `json:"manifest,omitempty"`

	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`

	// A reference to the secret holding this resource.
	// This is only relevant when the resource's kind is Secret.
	SecretName *string `json:"secretName,omitempty"`
}

type ResourceStatus struct {
	// True when the resource has been sync'd to the specified manifest.
	// This property latches: it will remain true if it has ever been true in the life of this resource.
	Reconciled bool `json:"reconciled,omitempty"`

	// nil if Eno is unable to determine the readiness of this resource.
	// Otherwise it is true when the resource is ready, false otherwise.
	// Like Reconciled, it latches and will never transition from true->false.
	Ready *bool `json:"ready,omitempty"`
}