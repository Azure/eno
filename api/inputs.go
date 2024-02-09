package api

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// InputWrapper wraps an arbitrary kubernetes resource to be passed as a
// synthesizer input. This is necessary so inputs can be referenced by a name
// that's independent of the underlying source.
type InputWrapper struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Resource          *unstructured.Unstructured `json:"resource"`
}
