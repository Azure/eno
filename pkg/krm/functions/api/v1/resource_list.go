package v1

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResourceList ResourceList is the input/output wire format for KRM functions.
//
// swagger:model ResourceList
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ResourceList struct {
	// apiVersion of ResourceList
	APIVersion string `json:"apiVersion"`

	// kind of ResourceList i.e. `ResourceList`
	Kind string `json:"kind"`

	// [input/output]
	// Items is a list of Kubernetes objects:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#types-kinds).
	//
	// A function will read this field in the input ResourceList and populate
	// this field in the output ResourceList.
	Items []*unstructured.Unstructured `json:"items"`

	// [input]
	// FunctionConfig is an optional Kubernetes object for passing arguments to a
	// function invocation.
	// +optional
	FunctionConfig *unstructured.Unstructured `json:"functionConfig,omitempty"`

	// [output]
	// Results is an optional list that can be used by function to emit results
	// for observability and debugging purposes.
	// +optional
	Results []*Result `json:"results,omitempty"`
}
