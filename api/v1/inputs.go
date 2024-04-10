package v1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// NewInput is used to create an `Input` with TypeMeta populated.
// This is required because `Input` is not a CRD, but we still want
// proper encoding/decoding via the Unstructured codec.
func NewInput(key string, res InputResource) Input {
	return Input{
		TypeMeta: metav1.TypeMeta{
			APIVersion: SchemeGroupVersion.String(),
			Kind:       "Input",
		},
		Key:      key,
		Resource: res,
	}
}

// Input is passed to Synthesis Pods at runtime and represents a bound ref.
type Input struct {
	metav1.TypeMeta `json:",inline"`
	// +required
	Key string `json:"key"`
	// +required
	Resource InputResource `json:"resource"`
}

type InputResource struct {
	// +required
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	// +required
	Kind  string `json:"kind"`
	Group string `json:"group"`
}

// Bindings map a specific Kubernetes resource to a ref exposed by a synthesizer.
// Compositions use bindings to populate inputs supported by their synthesizer.
type Binding struct {
	// Key determines which ref this binding binds to. Opaque.
	//
	// +required
	Key string `json:"key"`

	// +required
	Resource ResourceBinding `json:"resource"`
}

// A reference to a specific resource name and optionally namespace.
type ResourceBinding struct {
	// +required
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// Ref defines a synthesizer input.
// Inputs are typed using the Kubernetes API - they are just normal Kubernetes resources.
// The consumer (synthesizer) specifies the resource's kind/group,
// while the producer (composition) specifies a specific resource name/namespace.
//
// Compositions that use the synthesizer will be re-synthesized when the resource bound to this ref changes.
// Re-synthesis happens automatically while honoring the globally configured cooldown period.
type Ref struct {
	// Key corresponds to bindings to this ref.
	//
	// +required
	Key string `json:"key"`

	// +required
	Resource ResourceRef `json:"resource"`

	// Allows control over re-synthesis when inputs changed.
	// A non-deferred input will trigger a synthesis immediately, whereas a
	// deferred input will respect the cooldown period.
	Defer bool `json:"defer,omitempty"`
}

// A reference to a resource kind/group.
type ResourceRef struct {
	// +required
	Kind  string `json:"kind"`
	Group string `json:"group,omitempty"`
}
