package v1

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:object:root=true
type SynthesizerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Synthesizer `json:"items"`
}

// Synthesizers are any process that can run in a Kubernetes container that implements the [KRM Functions Specification](https://github.com/kubernetes-sigs/kustomize/blob/master/cmd/config/docs/api-conventions/functions-spec.md).
//
// Synthesizer processes are given some metadata about the composition they are synthesizing, and are expected
// to return a set of Kubernetes resources. Essentially they generate the desired state for a set of Kubernetes resources.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.image`
type Synthesizer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SynthesizerSpec   `json:"spec,omitempty"`
	Status SynthesizerStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="duration(self.execTimeout) <= duration(self.podTimeout)",message="podTimeout must be greater than execTimeout"
type SynthesizerSpec struct {
	// Copied opaquely into the container's image property.
	Image string `json:"image,omitempty"`

	// Copied opaquely into the container's command property.
	//
	// +kubebuilder:default={"synthesize"}
	Command []string `json:"command,omitempty"`

	// DEPRECATED
	// Timeout for each execution of the synthesizer command.
	//
	// +kubebuilder:default="10s"
	ExecTimeout *metav1.Duration `json:"execTimeout,omitempty"`

	// DEPRECATED
	// Pods are recreated after they've existed for at least the pod timeout interval.
	// This helps close the loop in failure modes where a pod may be considered ready but not actually able to run.
	//
	// +kubebuilder:default="2m"
	PodTimeout *metav1.Duration `json:"podTimeout,omitempty"`

	// Refs define the Synthesizer's input schema without binding it to specific
	// resources.
	Refs []Ref `json:"refs,omitempty"`

	// PodOverrides sets values in the pods used to execute this synthesizer.
	PodOverrides PodOverrides `json:"podOverrides,omitempty"`
}

type PodOverrides struct {
	Labels      map[string]string           `json:"labels,omitempty"`
	Annotations map[string]string           `json:"annotations,omitempty"`
	Resources   corev1.ResourceRequirements `json:"resources,omitempty"`
	Affinity    *corev1.Affinity            `json:"affinity,omitempty"`
}

type SynthesizerStatus struct {
}

// +kubebuilder:validation:XValidation:rule="has(self.name) || has(self.labelSelector)",message="at least one of name or labelSelector must be set"
type SynthesizerRef struct {
	Name          string                `json:"name,omitempty"`
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// Sentinel errors for synthesizer resolution.
var (
	// ErrNoMatchingSelector is returned when no synthesizers match the label selector.
	ErrNoMatchingSelector = errors.New("no synthesizers match the label selector")

	// ErrMultipleMatches is returned when more than one synthesizer matches the label selector.
	ErrMultipleMatches = errors.New("multiple synthesizers match the label selector")
)

// Resolve resolves a SynthesizerRef to a concrete Synthesizer.
//
// Precedence behavior: When both Name and LabelSelector are set in the ref,
// LabelSelector takes precedence and Name is ignored. This allows for more
// flexible matching when needed while maintaining backwards compatibility
// with name-based resolution.
//
// If the ref has a labelSelector, it lists all synthesizers matching the selector.
// Exactly one synthesizer must match; if zero match, ErrNoMatchingSelector is returned,
// and if more than one match, ErrMultipleMatches is returned.
//
// If labelSelector is not set, it uses the name field to get the synthesizer directly.
//
// Returns:
//   - The resolved Synthesizer if found
//   - nil, ErrNoMatchingSelector if no synthesizers match the label selector
//   - nil, ErrMultipleMatches if more than one synthesizer matches the label selector
//   - nil, error if there was an error during resolution
func (r *SynthesizerRef) Resolve(ctx context.Context, c client.Reader) (*Synthesizer, error) {
	// LabelSelector takes precedence over name
	if r.LabelSelector != nil {
		return r.resolveByLabel(ctx, c)
	}

	// Fallback to name-based resolution
	synth := &Synthesizer{}
	synth.Name = r.Name

	return synth, c.Get(ctx, client.ObjectKeyFromObject(synth), synth)
}

// resolveByLabel resolves a Synthesizer using a label selector.
// It lists all synthesizers matching the selector and returns the matching one.
// Exactly one synthesizer must match the selector.
//
// Returns:
//   - The resolved Synthesizer if exactly one matches
//   - nil, ErrNoMatchingSelector if no synthesizers match the selector
//   - nil, ErrMultipleMatches if more than one synthesizer matches the selector
//   - nil, error if there was an error during resolution
func (r *SynthesizerRef) resolveByLabel(ctx context.Context, c client.Reader) (*Synthesizer, error) {
	// Convert metav1.LabelSelector to labels.Selector
	selector, err := metav1.LabelSelectorAsSelector(r.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("converting label selector: %w", err)
	}

	// List all synthesizers matching the selector
	synthList := &SynthesizerList{}
	err = c.List(ctx, synthList, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		return nil, fmt.Errorf("listing synthesizers by label selector: %w", err)
	}

	// Handle results based on match count
	switch len(synthList.Items) {
	case 0:
		return nil, ErrNoMatchingSelector
	case 1:
		return &synthList.Items[0], nil
	default:
		return nil, ErrMultipleMatches
	}
}
