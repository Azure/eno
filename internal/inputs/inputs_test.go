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
		{
			Name: "Optional ref missing should not block synthesis",
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
						{Key: "key2", Optional: true},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "Required ref missing should block synthesis",
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
						{Key: "key2", Optional: false},
					},
				},
			},
			Expectation: false,
		},
		{
			Name: "All optional refs missing should allow synthesis",
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
					Refs: []apiv1.Ref{
						{Key: "key1", Optional: true},
						{Key: "key2", Optional: true},
					},
				},
			},
			Expectation: true,
		},
		{
			Name: "Optional ref present in InputRevisions",
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
						{Key: "key2", Optional: true},
					},
				},
			},
			Expectation: true,
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

func TestMissing(t *testing.T) {
	tests := []struct {
		Name        string
		Composition apiv1.Composition
		Synthesizer apiv1.Synthesizer
		Expectation []string
	}{
		{
			Name: "All required refs present",
			Composition: apiv1.Composition{
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
			Expectation: nil,
		},
		{
			Name: "One required ref missing",
			Composition: apiv1.Composition{
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
			Expectation: []string{"key2"},
		},
		{
			Name: "Multiple required refs missing reported in spec order",
			Composition: apiv1.Composition{
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
						{Key: "key2"},
						{Key: "key3"},
					},
				},
			},
			Expectation: []string{"key1", "key3"},
		},
		{
			Name: "Optional missing ref is not reported",
			Composition: apiv1.Composition{
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
						{Key: "key2", Optional: true},
					},
				},
			},
			Expectation: nil,
		},
		{
			Name: "Required missing while optional missing - only required reported",
			Composition: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2", Optional: true},
					},
				},
			},
			Expectation: []string{"key1"},
		},
		{
			Name: "No refs declared",
			Composition: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{},
				},
			},
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{Refs: []apiv1.Ref{}},
			},
			Expectation: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := Missing(&tt.Synthesizer, &tt.Composition)
			assert.Equal(t, tt.Expectation, result)
			// Consistency check: Exist iff Missing is empty.
			assert.Equal(t, len(tt.Expectation) == 0, Exist(&tt.Synthesizer, &tt.Composition))
		})
	}
}

func TestExpected(t *testing.T) {
	tests := []struct {
		Name        string
		Synthesizer apiv1.Synthesizer
		Expectation []string
	}{
		{
			Name: "All required refs",
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2"},
					},
				},
			},
			Expectation: []string{"key1", "key2"},
		},
		{
			Name: "Optional refs excluded",
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1"},
						{Key: "key2", Optional: true},
						{Key: "key3"},
					},
				},
			},
			Expectation: []string{"key1", "key3"},
		},
		{
			Name: "All optional refs",
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "key1", Optional: true},
						{Key: "key2", Optional: true},
					},
				},
			},
			Expectation: nil,
		},
		{
			Name: "No refs declared",
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{Refs: []apiv1.Ref{}},
			},
			Expectation: nil,
		},
		{
			Name: "Spec order is preserved",
			Synthesizer: apiv1.Synthesizer{
				Spec: apiv1.SynthesizerSpec{
					Refs: []apiv1.Ref{
						{Key: "c"},
						{Key: "a"},
						{Key: "b"},
					},
				},
			},
			Expectation: []string{"c", "a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := Expected(&tt.Synthesizer)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}

func TestMismatched(t *testing.T) {
	revision1 := 1
	revision2 := 2

	tests := []struct {
		Name        string
		Input       apiv1.Composition
		Synth       apiv1.Synthesizer
		Expectation []MismatchedInput
	}{
		{
			Name: "No revisions",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{},
				},
			},
			Expectation: nil,
		},
		{
			Name: "All revisions match",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "a", Revision: &revision1},
						{Key: "b", Revision: &revision1},
					},
				},
			},
			Expectation: nil,
		},
		{
			Name: "One lagging behind",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "a", Revision: &revision1},
						{Key: "b", Revision: &revision2},
						{Key: "c", Revision: &revision1},
					},
				},
			},
			Expectation: []MismatchedInput{
				{Key: "a", Revision: revision1, MaxRevision: revision2},
				{Key: "c", Revision: revision1, MaxRevision: revision2},
			},
		},
		{
			Name: "Nil revision is not reported as mismatch",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "a", Revision: &revision1},
						{Key: "b", Revision: nil},
					},
				},
			},
			Expectation: nil,
		},
		{
			Name: "Stale synthesizer generation",
			Input: apiv1.Composition{
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "a", SynthesizerGeneration: ptr.To(int64(122))},
					},
				},
			},
			Synth: apiv1.Synthesizer{
				ObjectMeta: metav1.ObjectMeta{Generation: 123},
			},
			Expectation: []MismatchedInput{
				{Key: "a", SynthesizerGeneration: 122},
			},
		},
		{
			Name: "Stale composition generation",
			Input: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Generation: 5},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "a", CompositionGeneration: ptr.To(int64(4))},
					},
				},
			},
			Expectation: []MismatchedInput{
				{Key: "a", CompositionGeneration: 4},
			},
		},
		{
			Name: "Stale gens and revision mismatch combined",
			Input: apiv1.Composition{
				ObjectMeta: metav1.ObjectMeta{Generation: 5},
				Status: apiv1.CompositionStatus{
					InputRevisions: []apiv1.InputRevisions{
						{Key: "a", Revision: &revision1, CompositionGeneration: ptr.To(int64(4))},
						{Key: "b", Revision: &revision2, CompositionGeneration: ptr.To(int64(5))},
					},
				},
			},
			Expectation: []MismatchedInput{
				{Key: "a", Revision: revision1, MaxRevision: revision2, CompositionGeneration: 4},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			result := Mismatched(&tt.Synth, &tt.Input, tt.Input.Status.InputRevisions)
			assert.Equal(t, tt.Expectation, result)
			// Consistency check: OutOfLockstep iff Mismatched is non-empty.
			assert.Equal(t, len(tt.Expectation) > 0, OutOfLockstep(&tt.Synth, &tt.Input, tt.Input.Status.InputRevisions))
		})
	}
}
