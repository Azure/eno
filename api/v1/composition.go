package v1

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	enoAzureOperationIDKey                   = "eno.azure.io/operationID"
	enoAzureOperationOrigin                  = "eno.azure.io/operationOrigin"
	OperationIdKey                    string = "operationID"
	OperationOrigionKey               string = "operationOrigin"
	CircularDependencyReason          string = "CircularDependency"
	WaitingOnDependentsReason         string = "WaitingOnDependents"
	WaitingOnDependenciesReason       string = "WaitingOnDependencies"
	WaitingOnDependencyNotFoundReason string = "DependencyNotFound"
	WaitingOnDependencyNotReadyReason string = "DependencyNotReady"
	WaitingOnDependentsDeletedReason  string = "DependentNotDeleted"
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

	// Declare dependencies on other compositions by name and namespace. A composition can have at most 50 dependencies
	// Compositions will not be scheduled for synthesis until all required
	// dependencies have CurrentSynthesis.Ready != nil
	// Deletion is blocked until all non-optional dependents are fully removed.
	// +kubebuilder:validation:MaxItems:=50
	DependsOn []CompositionDependency `json:"dependsOn,omitempty"`
}

type CompositionDependency struct {
	// Name of the dependency composition
	Name string `json:"name,omitempty"`

	//Namespace of the dependency composition
	Namespace string `json:"namespace,omitempty"`
}

type CompositionStatus struct {
	Simplified        *SimplifiedStatus `json:"simplified,omitempty"`
	InFlightSynthesis *Synthesis        `json:"inFlightSynthesis,omitempty"`
	CurrentSynthesis  *Synthesis        `json:"currentSynthesis,omitempty"`
	PreviousSynthesis *Synthesis        `json:"previousSynthesis,omitempty"`
	InputRevisions    []InputRevisions  `json:"inputRevisions,omitempty"`

	// Set when composition is blocked by dependency constraints. Cleared when unblock
	DependencyStatus *DependencyStatus `json:"dependencyStatus,omitempty"`
}

// DependencyStatus holds the information regarding the composition's dependencies.
type DependencyStatus struct {
	// Blocked is true when the composition cannot proceed due to dependencies/dependents
	Blocked bool `json:"blocked,omitempty"`

	// Reason "WaitingOnDependencies", "WaitingOnDependents"(Deletion), "CircularDependencies"
	Reason string `json:"reason,omitempty"`

	//BlockedBy: References to the compositions causing the block
	BlockedBy []BlockedByRef `json:"blockedBy,omitempty"`
}

type BlockedByRef struct {
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Reason    string `json:"reason,omitempty"` // "NotFound", "NotDeleted", "NotReady"
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
	CompositionGeneration *int64 `json:"compositionGeneration,omitempty"`
	IgnoreSideEffects     *bool  `json:"ignoreSideEffects,omitempty"`
}

func NewInputRevisions(obj client.Object, refKey string) *InputRevisions {
	ir := InputRevisions{
		Key:             refKey,
		ResourceVersion: obj.GetResourceVersion(),
	}
	if rev, _ := strconv.Atoi(obj.GetAnnotations()["eno.azure.io/revision"]); rev != 0 {
		ir.Revision = &rev
	}
	if rev, _ := strconv.ParseInt(obj.GetAnnotations()["eno.azure.io/synthesizer-generation"], 10, 64); rev != 0 {
		ir.SynthesizerGeneration = &rev
	}
	if rev, _ := strconv.ParseInt(obj.GetAnnotations()["eno.azure.io/composition-generation"], 10, 64); rev != 0 {
		ir.CompositionGeneration = &rev
	}
	if val, err := strconv.ParseBool(obj.GetAnnotations()["eno.azure.io/ignore-side-effects"]); err == nil {
		ir.IgnoreSideEffects = &val
	}
	return &ir
}

// Less reports whetehr i should be ordered before b
// both revisiions must share the same key. We can not compare across differnt keys
// Below is the ordering rules
// 1. If both sides have an explicit Revision, compare by Revision
// 2. If ResourceVersions are equal, check if IgnoreSideEffects annotation is present. If ignore side effects variant is preferred.
// 3. If ResourceVersions aren't parseable as ints, fall back to treating i as "less" so comparison degrades gracefully.
func (i *InputRevisions) Less(b InputRevisions) bool {
	if i.Key != b.Key {
		panic(fmt.Sprintf("cannot compare input revisions for different keys: %s != %s", i.Key, b.Key))
	}
	if i.Revision != nil && b.Revision != nil {
		return *i.Revision < *b.Revision
	}
	// When ResourceVersions match, prefer the revision that has IgnoreSideEffects=true
	// A nil IgnoreSideEffects is treated as false, so we only return true when i is
	// explicitly true and b is either nil or false.
	if i.ResourceVersion == b.ResourceVersion {
		return i.IgnoreSideEffects != nil && *i.IgnoreSideEffects &&
			(b.IgnoreSideEffects == nil || !*b.IgnoreSideEffects)
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

func (c *Composition) GetAzureOperationID() string {
	opId := c.Annotations[enoAzureOperationIDKey]
	if opId == "" {
		opId = getSynthesisEnvValue(&c.Spec, OperationIdKey)
	}

	return opId
}

func (c *Composition) GetAzureOperationOrigin() string {
	opOrigin := c.Annotations[enoAzureOperationOrigin]
	if opOrigin == "" {
		opOrigin = getSynthesisEnvValue(&c.Spec, OperationOrigionKey)
	}
	return opOrigin
}

func getSynthesisEnvValue(spec *CompositionSpec, key string) string {
	synthesisEnv := spec.SynthesisEnv
	for _, envVar := range synthesisEnv {
		if envVar.Name == key {
			return envVar.Value
		}
	}
	return ""
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
