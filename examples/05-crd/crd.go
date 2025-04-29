// +kubebuilder:object:generate=true
// +groupName=example.azure.io
// +versionName=v1
package main

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen object crd paths=./...

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "example.azure.io", Version: "v1"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

func init() {
	SchemeBuilder.Register(&ExampleList{}, &Example{})
}

// +kubebuilder:object:root=true
type ExampleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Example `json:"items"`
}

// +kubebuilder:object:root=true
type Example struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExampleSpec   `json:"spec,omitempty"`
	Status ExampleStatus `json:"status,omitempty"`
}

type ExampleSpec struct {
	StringValue string `json:"stringValue,omitempty"`
}

type ExampleStatus struct {
}
