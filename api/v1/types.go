// +kubebuilder:object:generate=true
// +groupName=eno.azure.io
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "eno.azure.io", Version: "v1"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

func init() {
	SchemeBuilder.Register(&CompositionList{}, &Composition{})
	SchemeBuilder.Register(&GeneratedResourceList{}, &GeneratedResource{})
}

//go:generate controller-gen object crd rbac:roleName=resourceprovider paths=./...

// +kubebuilder:object:root=true
type CompositionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Composition `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type Composition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CompositionSpec   `json:"spec,omitempty"`
	Status CompositionStatus `json:"status,omitempty"`
}

type CompositionSpec struct {
	Revision          int64            `json:"revision,omitempty"`
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`
	Generator         *Generator       `json:"generator,omitempty"`
	Inputs            []InputRef       `json:"inputs,omitempty"`
}

type Generator struct {
	// TODO: Podspec overrides? At least allow setting resource limits? Service accounts?
	Image string `json:"image,omitempty"`
}

type InputRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

const (
	CompositionGeneratedConditionType  = "eno.azure.io/composition-generated"
	CompositionReconciledConditionType = "eno.azure.io/composition-reconciled"
)

type CompositionStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type GeneratedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GeneratedResource `json:"items"`
}

// +kubebuilder:object:root=true
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
}

type GeneratedResourceStatus struct {
	// DerivedGeneration is the generation of the Composition resource that this resource was generated from.
	DerivedGeneration int64 `json:"derivedGeneration,omitempty"`
}
