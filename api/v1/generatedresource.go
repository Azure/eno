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
	// The product of unfortunate type names and naming conventions.
	// It refers to the metadata.generation property of the Generation resource that caused this resource to be created.
	GenerationGeneration int64 `json:"generationGeneration,omitempty"`

	Resources []GeneratedResourceSpec `json:"resources,omitempty"`
}

type GeneratedResourceSliceStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Elements of resources correspond in index to those in spec.resources at the observed generation.
	Resources []GeneratedResourceStatus `json:"resources,omitempty"`
}

type GeneratedResourceSpec struct {
	// +required
	Manifest string `json:"manifest,omitempty"`

	PreviousManifest *string `json:"previousManifest,omitempty"`

	// A reference to the secret holding this generated resource.
	// This is only relevant when this resource's kind is Secret.
	SecretName *string `json:"secretName,omitempty"`
}

type GeneratedResourceStatus struct {
	// True when this representation of the given resource no longer needs to be persisted.
	// When all resources in this slice have been released, the slice can safely be deleted.
	Released bool `json:"released,omitempty"`

	// True when the resource has been sync'd to the specified manifest.
	// This property latches: it will remain true if it has ever been true in the life of this generated resource.
	Reconciled bool `json:"reconciled,omitempty"`

	// nil if Eno is unable to determine the readiness of this resource.
	// Otherwise it is true when the resource is ready, false otherwise.
	// Like Reconciled, it latches and will never transition from true->false.
	Ready *bool `json:"ready,omitempty"`

	// The last seen resource version of the sync'd resource.
	ResourceVersion *string `json:"resourceVersion,omitempty"`
}
