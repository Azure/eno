// +kubebuilder:object:generate=true
// +groupName=eno.azure.io
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

//go:generate controller-gen object crd rbac:roleName=resourceprovider paths=./...

// Requires https://github.com/elastic/crd-ref-docs
//
//go:generate crd-ref-docs --source-path=./ --config=docsconfig.yaml --renderer=markdown --output-path=../../docs/api.md

var (
	SchemeGroupVersion = schema.GroupVersion{Group: "eno.azure.io", Version: "v1"}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

func init() {
	SchemeBuilder.Register(&SynthesizerList{}, &Synthesizer{})
	SchemeBuilder.Register(&CompositionList{}, &Composition{})
	SchemeBuilder.Register(&SymphonyList{}, &Symphony{})
	SchemeBuilder.Register(&ResourceSliceList{}, &ResourceSlice{})
}
