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

type ResourceSliceSpec struct {
	CompositionGeneration int64      `json:"compositionGeneration,omitempty"`
	SynthesisUUID         string     `json:"synthesisUUID,omitempty"`
	Attempt               int        `json:"attempt,omitempty"`
	Resources             []Manifest `json:"resources,omitempty"`
}

type Manifest struct {
	Manifest string `json:"manifest,omitempty"`

	// Deleted is true when this manifest represents a "tombstone" - a resource that should no longer exist.
	Deleted bool `json:"deleted,omitempty"`
}

type ResourceSliceStatus struct {
	// Elements of resources correspond in index to those in spec.resources at the observed generation.
	Resources []ResourceState `json:"resources,omitempty"`
}

type ResourceState struct {
	Reconciled bool         `json:"reconciled,omitempty"`
	Ready      *metav1.Time `json:"ready,omitempty"`
	Deleted    bool         `json:"deleted,omitempty"`
}

func (r *ResourceState) Equal(rr *ResourceState) bool {
	if r == nil {
		return rr == nil
	}
	if rr == nil {
		return false
	}
	if r.Reconciled != rr.Reconciled || r.Deleted != rr.Deleted {
		return false
	}
	if r.Ready == nil {
		return rr.Ready == nil
	}
	if rr.Ready == nil {
		return r.Ready == nil
	}
	return r.Ready.Equal(rr.Ready)
}

type ResourceSliceRef struct {
	Name string `json:"name,omitempty"`
}
