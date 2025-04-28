package v1

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
type CompositionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Composition `json:"items"`
}

// Compositions represent a collection of related, synthesized resources.
//
// For example: when managing Postgres with Eno, one would create a composition
// per distinct instance of Postgres, all referencing a single synthesizer resource.
//
// Changing the spec of a composition will result in re-synthesis.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Synthesizer",type=string,JSONPath=`.spec.synthesizer.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.status.currentSynthesis.synthesized`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.status.simplified.status`
// +kubebuilder:printcolumn:name="Error",type=string,JSONPath=`.status.simplified.error`
type Composition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CompositionSpec   `json:"spec,omitempty"`
	Status CompositionStatus `json:"status,omitempty"`
}

type CompositionSpec struct {
	// Compositions are synthesized by a Synthesizer, referenced by name.
	Synthesizer SynthesizerRef `json:"synthesizer,omitempty"`

	// Synthesizers can accept Kubernetes resources as inputs.
	// Bindings allow compositions to specify which resource to use for a particular input "reference".
	// Declaring extra bindings not (yet) supported by the synthesizer is valid.
	Bindings []Binding `json:"bindings,omitempty"`

	// SynthesisEnv
	// A set of environment variables that will be made available inside the synthesis Pod.
	// +kubebuilder:validation:MaxItems:=500
	SynthesisEnv []EnvVar `json:"synthesisEnv,omitempty"`
}

type CompositionStatus struct {
	Simplified        *SimplifiedStatus `json:"simplified,omitempty"`
	InFlightSynthesis *Synthesis        `json:"inFlightSynthesis,omitempty"`
	CurrentSynthesis  *Synthesis        `json:"currentSynthesis,omitempty"`
	PreviousSynthesis *Synthesis        `json:"previousSynthesis,omitempty"`
	InputRevisions    []InputRevisions  `json:"inputRevisions,omitempty"`
}

type SimplifiedStatus struct {
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *SimplifiedStatus) String() string {
	if s == nil {
		return "Nil"
	}
	if s.Error == "" {
		return s.Status
	}
	return fmt.Sprintf("%s (error: %s)", s.Status, s.Error)
}

// A synthesis is the result of synthesizing a composition.
// In other words: it's a collection of resources returned from a synthesizer.
type Synthesis struct {
	// A random UUID scoped to this particular synthesis operation.
	// Used internally for strict ordering semantics.
	UUID string `json:"uuid,omitempty"`

	// The value of the composition's metadata.generation at the time the synthesis began.
	// This is a min i.e. a newer composition may have been used.
	ObservedCompositionGeneration int64 `json:"observedCompositionGeneration,omitempty"`

	// The value of the synthesizer's metadata.generation at the time the synthesis began.
	// This is a min i.e. a newer composition may have been used.
	ObservedSynthesizerGeneration int64 `json:"observedSynthesizerGeneration,omitempty"`

	// Initialized is set when the synthesis process is initiated.
	Initialized *metav1.Time `json:"initialized,omitempty"`

	// Time at which the most recent synthesizer pod was created.
	PodCreation *metav1.Time `json:"podCreation,omitempty"`

	// Time at which the synthesis completed i.e. resourceSlices was written
	Synthesized *metav1.Time `json:"synthesized,omitempty"`

	// Time at which the synthesis's resources were reconciled into real Kubernetes resources.
	Reconciled *metav1.Time `json:"reconciled,omitempty"`

	// Time at which the synthesis's reconciled resources became ready.
	Ready *metav1.Time `json:"ready,omitempty"`

	// Canceled signals that any running synthesis pods should be deleted,
	// and new synthesis pods should never be created for this synthesis UUID.
	Canceled *metav1.Time `json:"canceled,omitempty"`

	// Counter used internally to calculate back off when retrying failed syntheses.
	Attempts int `json:"attempts,omitempty"`

	// References to every resource slice that contains the resources comprising this synthesis.
	// Immutable.
	ResourceSlices []*ResourceSliceRef `json:"resourceSlices,omitempty"`

	// Results are passed through opaquely from the synthesizer's KRM function.
	Results []Result `json:"results,omitempty"`

	// InputRevisions contains the versions of the input resources that were used for this synthesis.
	InputRevisions []InputRevisions `json:"inputRevisions,omitempty"`

	// Deferred is true when this synthesis was caused by a change to either the synthesizer
	// or an input with a ref that sets `Defer == true`.
	Deferred bool `json:"deferred,omitempty"`
}

type Result struct {
	Message  string            `json:"message,omitempty"`
	Severity string            `json:"severity,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
}

type InputRevisions struct {
	Key                   string `json:"key,omitempty"`
	ResourceVersion       string `json:"resourceVersion,omitempty"`
	Revision              *int   `json:"revision,omitempty"`
	SynthesizerGeneration *int64 `json:"synthesizerGeneration,omitempty"`
}

func (i *InputRevisions) Less(b InputRevisions) bool {
	if i.Key != b.Key {
		panic(fmt.Sprintf("cannot compare input revisions for different keys: %s != %s", i.Key, b.Key))
	}
	if i.Revision != nil && b.Revision != nil {
		return *i.Revision < *b.Revision
	}
	if i.ResourceVersion == b.ResourceVersion {
		return false
	}
	iInt, iErr := strconv.Atoi(i.ResourceVersion)
	bInt, bErr := strconv.Atoi(b.ResourceVersion)
	if iErr != nil || bErr != nil {
		return true // effectively fall back to equality comparison if they aren't ints (shouldn't be possible)
	}
	return iInt < bInt
}

func (s *CompositionStatus) GetCurrentSynthesisUUID() string {
	if s.CurrentSynthesis == nil {
		return ""
	}
	return s.CurrentSynthesis.UUID
}

func (c *Composition) ShouldIgnoreSideEffects() bool {
	return c.Annotations["eno.azure.io/ignore-side-effects"] == "true"
}

func (c *Composition) Synthesizing() bool {
	return c.Status.InFlightSynthesis != nil && c.Status.InFlightSynthesis.Canceled == nil
}

func (c *Composition) EnableIgnoreSideEffects() {
	anno := c.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	anno["eno.azure.io/ignore-side-effects"] = "true"
	c.SetAnnotations(anno)
}

const forceResynthesisAnnotation = "eno.azure.io/force-resynthesis"

func (c *Composition) ForceResynthesis() {
	anno := c.GetAnnotations()
	if anno == nil {
		anno = map[string]string{}
	}
	anno[forceResynthesisAnnotation] = c.Status.getLatestSynthesisUUID()
	c.SetAnnotations(anno)
}

func (c *Composition) ShouldForceResynthesis() bool {
	val, ok := c.GetAnnotations()[forceResynthesisAnnotation]
	return ok && val == c.Status.getLatestSynthesisUUID()
}

func (c *Composition) ShouldOrphanResources() bool {
	return c.Annotations["eno.azure.io/deletion-strategy"] == "orphan"
}

func (s *CompositionStatus) getLatestSynthesisUUID() string {
	if s.InFlightSynthesis != nil {
		return s.InFlightSynthesis.UUID
	}
	if s.CurrentSynthesis != nil {
		return s.CurrentSynthesis.UUID
	}
	return ""
}
