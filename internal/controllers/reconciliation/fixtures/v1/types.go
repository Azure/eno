// +kubebuilder:object:generate=true
// +groupName=enotest.azure.io
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// When re-generating also update any *-old.yaml files (see their comments for details)
//go:generate controller-gen object crd rbac:roleName=resourceprovider paths=./...

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "enotest.azure.io", Version: "v1"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

func init() {
	SchemeBuilder.Register(&TestResourceList{}, &TestResource{})
}

// +kubebuilder:object:root=true
type TestResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TestResource `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
type TestResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TestResourceSpec   `json:"spec,omitempty"`
	Status TestResourceStatus `json:"status,omitempty"`
}

type TestResourceSpec struct {
	Values []*TestValue `json:"values,omitempty"`
}

type TestValue struct {
	Int int `json:"int,omitempty"`
}

type TestResourceStatus struct {
}
