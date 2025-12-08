// +kubebuilder:object:generate=true
// +groupName=eno.azure.io
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

//go:generate go run sigs.k8s.io/controller-tools/cmd/controller-gen object crd rbac:roleName=resourceprovider paths=./...

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "eno.azure.io", Version: "v1"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

func init() {
	SchemeBuilder.Register(&SynthesizerList{}, &Synthesizer{})
	SchemeBuilder.Register(&CompositionList{}, &Composition{})
	SchemeBuilder.Register(&SymphonyList{}, &Symphony{})
	SchemeBuilder.Register(&ResourceSliceList{}, &ResourceSlice{})
	SchemeBuilder.Register(&InputMirrorList{}, &InputMirror{})
}
