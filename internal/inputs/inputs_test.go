package inputs

import (
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestExist(t *testing.T) {
	// GPT generated - beware!!!
	tests := []struct {
		Name        string
		Composition apiv1.Composition
		Synthesizer apiv1.Synthesizer
		Expectation bool
	}{
		{
			Name: "All required refs are bound and exist in InputRevisions",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
						{Key: "key2"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key1"},
						{Key: "key2"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2"},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "Missing InputRevisions for a required ref",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key1"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2"},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "Binding exists but not required by synthesizer",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
						{Key: "key3"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key1"},
						{Key: "key3"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "Implied binding missing in InputRevisions",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key1"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2", Resource: apiv1.ResourceRef{Name: "resource2"}},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "All implied bindings exist in InputRevisions",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key1"},
						{Key: "key2"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2", Resource: apiv1.ResourceRef{Name: "resource2"}},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "No bindings or refs",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{},
				},
			},
			Expectation: true,
		},
		{
			Name: "Extra InputRevisions not required by synthesizer",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key1"},
						{Key: "key3"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "Bound but missing",
			Composition: apiv1.Composition{
				Spec: apiv1.CompositionSpec{
					Bindings: []apiv1.Binding{
						{Key: "key1"},
					},
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "key2"},
					},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
					},
				},
			},
			Expectation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := Exist(&tt.Synthesizer, &tt.Composition)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}

func TestOutOfLockstep(t *testing.T) {
	revision1 := 1
	revision2 := 2

	tests := []struct {
		Name        string
		Input       apiv1.Composition
		Synth       apiv1.Synthesizer
		Expectation bool
	}{
		{
			Name: "No revisions",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{},
				},
			},
			Expectation: false,
		},
		{
			Name: "All nil revisions",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Revision: nil, ResourceVersion: "1"},
						{Revision: nil, ResourceVersion: "2"},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "One lagging behind",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
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
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
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
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
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
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
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
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{{
						SynthesizerGeneration: ptr.To(int64(122)),
					}},
				},
			},
			Synth: apiv1.Synthesizer{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
			},
			Expectation: true,
		},
		{
			Name: "At pace with synth",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{{
						SynthesizerGeneration: ptr.To(int64(123)),
					}},
				},
			},
			Synth: apiv1.Synthesizer{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
			},
			Expectation: false,
		},
		{
			Name: "Ahead of synth",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{{
						SynthesizerGeneration: ptr.To(int64(124)),
					}},
				},
			},
			Synth: apiv1.Synthesizer{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
			},
			Expectation: false,
		},
		{
			Name: "Lagging behind comp",
			Input: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{{
						CompositionGeneration: ptr.To(int64(122)),
					}},
				},
			},
			Expectation: true,
		},
		{
			Name: "At pace with comp",
			Input: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{{
						CompositionGeneration: ptr.To(int64(123)),
					}},
				},
			},
			Expectation: false,
		},
		{
			Name: "Ahead of comp",
			Input: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{
					Generation: 123,
				},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{{
						CompositionGeneration: ptr.To(int64(124)),
					}},
				},
			},
			Expectation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := OutOfLockstep(&tt.Synth, &tt.Input, tt.Input.Status.InputRevisions)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}
