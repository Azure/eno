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
	Key             string        `json:"key"`
	Resource        InputResource `json:"resource"`
}

type InputResource struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind"`
	Group     string `json:"group"`
}

// Bindings map a specific Kubernetes resource to a ref exposed by a synthesizer.
// Compositions use bindings to populate inputs supported by their synthesizer.
type Binding struct {
	// Key determines which ref this binding binds to. Opaque.
	Key string `json:"key"`

	Resource ResourceBinding `json:"resource"`
}

// A reference to a specific resource name and optionally namespace.
type ResourceBinding struct {
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
	Key string `json:"key"`

	Resource ResourceRef `json:"resource"`

	// Allows control over re-synthesis when inputs changed.
	// A non-deferred input will trigger a synthesis immediately, whereas a
	// deferred input will respect the cooldown period.
	Defer bool `json:"defer,omitempty"`
}

// A reference to a resource kind/group.
type ResourceRef struct {
	Group   string `json:"group,omitempty"`
	Version string `json:"version,omitempty"`
	Kind    string `json:"kind"`

	// If set, name and namespace form an "implicit binding", i.e. a ref that is bound to
	// a specific resource without a corresponding binding on the composition resource.
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}
