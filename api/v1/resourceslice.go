package v1

import (
	"encoding/json"
	"path"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionResourceApplied = "ResourceApplied"
	ConditionResourceReady   = "ResourceReady"
)

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
	SynthesisUUID string     `json:"synthesisUUID,omitempty"`
	Resources     []Manifest `json:"resources,omitempty"`
}

type Manifest struct {
	Manifest string `json:"manifest,omitempty"`

	// Deleted is true when this manifest represents a "tombstone" - a resource that should no longer exist.
	Deleted bool `json:"deleted,omitempty"`

	// ParsedKind and ParsedName are populated by the informer cache Transform so the per-resource identifier survives the manifest
	// stripping done to bound cache memory. They are never serialized to the API Server and never appera in the CRD schema
	ParsedKind string `json:"-"`
	ParsedName string `json:"-"`
}

type ResourceSliceStatus struct {
	// Elements of resources correspond in index to those in spec.resources at the observed generation.
	Resources []ResourceState `json:"resources,omitempty"`
}

type ResourceState struct {
	Reconciled          bool         `json:"reconciled,omitempty"`
	Ready               *metav1.Time `json:"ready,omitempty"`
	Deleted             bool         `json:"deleted,omitempty"`
	ReconciliationError *string      `json:"reconciliationError,omitempty"`
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

// IdentifierAt returns "kind/name" for the manifest at the same index in the slice's spec, or "" if the manifest can not be parsed.
// Prefers pre-parsed fields by the cache Transforms and then falls back to parsing the raw json manifest.
func (s *ResourceSlice) IdentifierAt(idx int) string {
	if idx < 0 || idx >= len(s.Spec.Resources) {
		return ""
	}

	r := s.Spec.Resources[idx]
	if r.ParsedKind != "" && r.ParsedName != "" {
		return path.Join(r.ParsedKind, r.ParsedName)
	}

	if r.Manifest == "" {
		return ""
	}

	var shallowCopy struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}

	if err := json.Unmarshal([]byte(r.Manifest), &shallowCopy); err != nil {
		return ""
	}
	if shallowCopy.Kind == "" || shallowCopy.Metadata.Name == "" {
		return ""
	}
	return path.Join(shallowCopy.Kind, shallowCopy.Metadata.Name)
}
