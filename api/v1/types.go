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
	SchemeBuilder.Register(&GeneratorList{}, &Generator{})
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
	Generator         *GeneratorRef    `json:"generator,omitempty"`
	Inputs            []InputRef       `json:"inputs,omitempty"`
}

type GeneratorRef struct {
	Name string `json:"name,omitempty"`
}

type InputRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name,omitempty"`
}

const (
	ReconciledConditionType = "eno.azure.io/reconciled"
	ReadyConditionType      = "eno.azure.io/ready"
)

type CompositionStatus struct {
	ObservedGeneration     int64              `json:"observedGeneration,omitempty"`
	GeneratedResourceCount int64              `json:"generatedResourceCount,omitempty"`
	Conditions             []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type GeneratedResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GeneratedResource `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
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
	DerivedGeneration int64            `json:"derivedGeneration,omitempty"`
}

type GeneratedResourceStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type GeneratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Generator `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type Generator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GeneratorSpec   `json:"spec,omitempty"`
	Status GeneratorStatus `json:"status,omitempty"`
}

type GeneratorSpec struct {
	Image string `json:"image,omitempty"`
}

type GeneratorStatus struct {
}
