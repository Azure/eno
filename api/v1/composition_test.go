package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestInputRevisionsEqual(t *testing.T) {
	revision1 := 1
	revision2 := 2
	tests := []struct {
		Name        string
		A           InputRevisions
		B           InputRevisions
		Expectation bool
	}{
		{
			Name: "Equal with nil revisions and same ResourceVersion",
			A: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "v1",
			},
			B: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "v1",
			},
			Expectation: true,
		},
		{
			Name: "Equal with non-nil revisions",
			A: InputRevisions{
				Key:             "key2",
				Revision:        &revision1,
				ResourceVersion: "v2",
			},
			B: InputRevisions{
				Key:             "key2",
				Revision:        &revision1,
				ResourceVersion: "v2",
			},
			Expectation: true,
		},
		{
			Name: "Not equal with different keys",
			A: InputRevisions{
				Key:             "key3",
				Revision:        &revision1,
				ResourceVersion: "v3",
			},
			B: InputRevisions{
				Key:             "key4",
				Revision:        &revision1,
				ResourceVersion: "v3",
			},
			Expectation: false,
		},
		{
			Name: "Not equal with different non-nil revisions",
			A: InputRevisions{
				Key:             "key5",
				Revision:        &revision1,
				ResourceVersion: "v5",
			},
			B: InputRevisions{
				Key:             "key5",
				Revision:        &revision2,
				ResourceVersion: "v5",
			},
			Expectation: false,
		},
		{
			Name: "Not equal with one nil and one non-nil revision",
			A: InputRevisions{
				Key:             "key6",
				Revision:        nil,
				ResourceVersion: "v6",
			},
			B: InputRevisions{
				Key:             "key6",
				Revision:        &revision1,
				ResourceVersion: "v6",
			},
			Expectation: false,
		},
		{
			Name: "Not equal with same keys but different ResourceVersions",
			A: InputRevisions{
				Key:             "key7",
				Revision:        nil,
				ResourceVersion: "v7",
			},
			B: InputRevisions{
				Key:             "key7",
				Revision:        nil,
				ResourceVersion: "v8",
			},
			Expectation: false,
		},
		{
			Name: "Same Revision, different ResourceVersion",
			A: InputRevisions{
				Key:             "key7",
				Revision:        &revision1,
				ResourceVersion: "v7",
			},
			B: InputRevisions{
				Key:             "key7",
				Revision:        &revision1,
				ResourceVersion: "v8",
			},
			Expectation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := tt.A.Equal(tt.B)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}

func TestSynthesisFailed(t *testing.T) {
	tests := []struct {
		Name        string
		Syn         Synthesis
		Expectation bool
	}{
		{
			Name:        "No results",
			Syn:         Synthesis{Results: []Result{}},
			Expectation: false,
		},
		{
			Name: "No errors in results",
			Syn: Synthesis{Results: []Result{
				{Severity: "info"},
				{Severity: "warning"},
			}},
			Expectation: false,
		},
		{
			Name: "One error in results",
			Syn: Synthesis{Results: []Result{
				{Severity: "info"},
				{Severity: "error"},
			}},
			Expectation: true,
		},
		{
			Name: "Multiple errors in results",
			Syn: Synthesis{Results: []Result{
				{Severity: "error"},
				{Severity: "error"},
			}},
			Expectation: true,
		},
		{
			Name: "Mixed severities with error",
			Syn: Synthesis{Results: []Result{
				{Severity: "info"},
				{Severity: "warning"},
				{Severity: "error"},
			}},
			Expectation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := tt.Syn.Failed()
			assert.Equal(t, tt.Expectation, result)
		})
	}
}

func TestCompositionInputsExist(t *testing.T) {
	tests := []struct {
		Name        string
		Comp        Composition
		Expectation bool
	}{
		{
			Name: "No bindings, no revisions",
			Comp: Composition{
				Spec:   CompositionSpec{Bindings: []Binding{}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{}},
			},
			Expectation: true,
		},
		{
			Name: "Bindings with matching revisions",
			Comp: Composition{
				Spec: CompositionSpec{Bindings: []Binding{
					{Key: "key1"},
					{Key: "key2"},
				}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{
					{Key: "key1"},
					{Key: "key2"},
				}},
			},
			Expectation: true,
		},
		{
			Name: "Bindings with matching revisions but missing ref",
			Comp: Composition{
				Spec: CompositionSpec{Bindings: []Binding{
					{Key: "key1"},
					{Key: "key5"},
				}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{
					{Key: "key1"},
					{Key: "key5"},
				}},
			},
			Expectation: true,
		},
		{
			Name: "Bindings with non-matching revisions",
			Comp: Composition{
				Spec: CompositionSpec{Bindings: []Binding{
					{Key: "key1"},
					{Key: "key3"},
				}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{
					{Key: "key1"},
					{Key: "key2"},
				}},
			},
			Expectation: false,
		},
		{
			Name: "Bindings with missing revisions",
			Comp: Composition{
				Spec: CompositionSpec{Bindings: []Binding{
					{Key: "key1"},
					{Key: "key2"},
				}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{
					{Key: "key1"},
				}},
			},
			Expectation: false,
		},
		{
			Name: "Empty bindings, non-empty revisions",
			Comp: Composition{
				Spec: CompositionSpec{Bindings: []Binding{}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{
					{Key: "key1"},
					{Key: "key2"},
				}},
			},
			Expectation: true,
		},
		{
			Name: "Non-empty bindings, empty revisions",
			Comp: Composition{
				Spec: CompositionSpec{Bindings: []Binding{
					{Key: "key1"},
				}},
				Status: CompositionStatus{InputRevisions: []InputRevisions{}},
			},
			Expectation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			s := &Synthesizer{}
			s.Spec.Refs = []Ref{{Key: "key1"}, {Key: "key2"}, {Key: "key3"}}
			result := tt.Comp.InputsExist(s)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}

func TestInputsMismatched(t *testing.T) {
	revision1 := 1
	revision2 := 2

	tests := []struct {
		Name        string
		Input       Composition
		Synth       Synthesizer
		Expectation bool
	}{
		{
			Name: "No revisions",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{},
				},
			},
			Expectation: false,
		},
		{
			Name: "All nil revisions",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{
						{Revision: nil, ResourceVersion: "1"},
						{Revision: nil, ResourceVersion: "2"},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "One lagging behind",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{
						{Revision: &revision1, ResourceVersion: "1"},
						{Revision: &revision2, ResourceVersion: "1"},
						{Revision: &revision1, ResourceVersion: "1"},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "Mixed nil and non-nil revisions",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{
						{Revision: &revision2, ResourceVersion: "1"},
						{Revision: nil, ResourceVersion: "1"},
						{Revision: &revision1, ResourceVersion: "1"},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "One matching, one nil revision",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{
						{Revision: &revision1, ResourceVersion: "1"},
						{Revision: &revision1, ResourceVersion: "1"},
						{Revision: nil, ResourceVersion: "1"},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "All revisions the same",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{
						{Revision: &revision1, ResourceVersion: "1"},
						{Revision: &revision1, ResourceVersion: "2"},
						{Revision: &revision1, ResourceVersion: "3"},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "Lagging behind synth",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{{
						SynthesizerGeneration: ptr.To(int64(122)),
					}},
				},
			},
			Synth: Synthesizer{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
			},
			Expectation: true,
		},
		{
			Name: "At pace with synth",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{{
						SynthesizerGeneration: ptr.To(int64(123)),
					}},
				},
			},
			Synth: Synthesizer{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
			},
			Expectation: false,
		},
		{
			Name: "Ahead of synth",
			Input: Composition{
				Status: CompositionStatus{
					InputRevisions: []InputRevisions{{
						SynthesizerGeneration: ptr.To(int64(124)),
					}},
				},
			},
			Synth: Synthesizer{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
			},
			Expectation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := tt.Input.InputsMismatched(&tt.Synth)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}
