package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// +kubebuilder:object:root=true
type InputMirrorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InputMirror `json:"items"`
}

// InputMirror stores a copy of a resource from an overlay cluster.
// It is created and managed by the OverlaySyncController based on Symphony.spec.overlayResourceRefs.
// Compositions can bind to InputMirrors just like any other resource.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.sourceResource.name`
// +kubebuilder:printcolumn:name="Synced",type=string,JSONPath=`.status.conditions[?(@.type=="Synced")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type InputMirror struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InputMirrorSpec   `json:"spec,omitempty"`
	Status InputMirrorStatus `json:"status,omitempty"`
}

type InputMirrorSpec struct {
	// Key matches the Symphony's overlayResourceRef key
	Key string `json:"key"`

	// SymphonyRef points to the owning Symphony
	SymphonyRef corev1.LocalObjectReference `json:"symphonyRef"`

	// SourceResource describes what resource to sync from the overlay
	SourceResource OverlayResourceSelector `json:"sourceResource"`
}

type InputMirrorStatus struct {
	// Data contains the actual resource data from the overlay cluster.
	// This is the full resource serialized as JSON.
	// +kubebuilder:pruning:PreserveUnknownFields
	Data *runtime.RawExtension `json:"data,omitempty"`

	// LastSyncTime records when the resource was last successfully synced
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// SyncGeneration tracks the source resource's resourceVersion
	SyncGeneration string `json:"syncGeneration,omitempty"`

	// Conditions describe the sync state
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// OverlayResourceSelector describes a resource to sync from an overlay cluster
type OverlayResourceSelector struct {
	// API Group of the resource (empty string for core API group)
	// +optional
	Group string `json:"group,omitempty"`

	// API Version of the resource
	Version string `json:"version"`

	// Kind of the resource (e.g., ConfigMap, Secret)
	Kind string `json:"kind"`

	// Name of the resource
	Name string `json:"name"`

	// Namespace of the resource (empty for cluster-scoped resources)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// OverlayResourceRef defines a resource to sync from an overlay cluster
type OverlayResourceRef struct {
	// Key that will be used to reference this input in Composition bindings.
	// This key maps to an auto-created InputMirror resource.
	Key string `json:"key"`

	// Resource specifies what to fetch from the overlay cluster
	Resource OverlayResourceSelector `json:"resource"`

	// SyncInterval determines how often to re-sync the resource.
	// +kubebuilder:default="5m"
	// +optional
	SyncInterval *metav1.Duration `json:"syncInterval,omitempty"`

	// Optional indicates that synthesis can proceed if this resource doesn't exist in the overlay.
	// +kubebuilder:default=false
	// +optional
	Optional bool `json:"optional,omitempty"`
}
