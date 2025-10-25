package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInputRevisionsLess(t *testing.T) {
	revision1 := 1
	revision2 := 2
	trueVal := true
	falseVal := false
	tests := []struct {
		Name        string
		A           InputRevisions
		B           InputRevisions
		Expectation bool
		ShouldPanic bool
	}{
		{
			Name: "nil revisions and same ResourceVersion",
			A: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "1",
			},
			B: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "1",
			},
			Expectation: false,
		},
		{
			Name: "nil revisions and < ResourceVersion",
			A: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "1",
			},
			B: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "2",
			},
			Expectation: true,
		},
		{
			Name: "nil revisions and invalid non-matching ResourceVersions",
			A: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "foo",
			},
			B: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "bar",
			},
			Expectation: true,
		},
		{
			Name: "nil revisions and invalid matching ResourceVersions",
			A: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "foo",
			},
			B: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "foo",
			},
			Expectation: false,
		},
		{
			Name: "nil revisions and > ResourceVersion",
			A: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "2",
			},
			B: InputRevisions{
				Key:             "key1",
				Revision:        nil,
				ResourceVersion: "1",
			},
			Expectation: false,
		},
		{
			Name: "non-nil revisions equal",
			A: InputRevisions{
				Key:             "key2",
				Revision:        &revision1,
				ResourceVersion: "2",
			},
			B: InputRevisions{
				Key:             "key2",
				Revision:        &revision1,
				ResourceVersion: "2",
			},
			Expectation: false,
		},
		{
			Name: "non-nil revisions >",
			A: InputRevisions{
				Key:             "key2",
				Revision:        &revision2,
				ResourceVersion: "2",
			},
			B: InputRevisions{
				Key:             "key2",
				Revision:        &revision1,
				ResourceVersion: "2",
			},
			Expectation: false,
		},
		{
			Name: "non-nil revisions <",
			A: InputRevisions{
				Key:             "key2",
				Revision:        &revision1,
				ResourceVersion: "2",
			},
			B: InputRevisions{
				Key:             "key2",
				Revision:        &revision2,
				ResourceVersion: "2",
			},
			Expectation: true,
		},
		{
			Name: "different keys",
			A: InputRevisions{
				Key:             "key3",
				Revision:        &revision1,
				ResourceVersion: "3",
			},
			B: InputRevisions{
				Key:             "key4",
				Revision:        &revision1,
				ResourceVersion: "3",
			},
			Expectation: false,
			ShouldPanic: true,
		},
		{
			Name: "one nil and one non-nil revision positive",
			A: InputRevisions{
				Key:             "key6",
				Revision:        nil,
				ResourceVersion: "6",
			},
			B: InputRevisions{
				Key:             "key6",
				Revision:        &revision1,
				ResourceVersion: "7",
			},
			Expectation: true,
		},
		{
			Name: "one nil and one non-nil revision negative",
			A: InputRevisions{
				Key:             "key6",
				Revision:        &revision1,
				ResourceVersion: "6",
			},
			B: InputRevisions{
				Key:             "key6",
				Revision:        nil,
				ResourceVersion: "6",
			},
			Expectation: false,
		},
		{
			Name: "same ResourceVersion with IgnoreSideEffects true vs false",
			A: InputRevisions{
				Key:               "key7",
				ResourceVersion:   "7",
				IgnoreSideEffects: &trueVal,
			},
			B: InputRevisions{
				Key:               "key7",
				ResourceVersion:   "7",
				IgnoreSideEffects: &falseVal,
			},
			Expectation: true,
		},
		{
			Name: "same ResourceVersion with IgnoreSideEffects false vs true",
			A: InputRevisions{
				Key:               "key8",
				ResourceVersion:   "8",
				IgnoreSideEffects: &falseVal,
			},
			B: InputRevisions{
				Key:               "key8",
				ResourceVersion:   "8",
				IgnoreSideEffects: &trueVal,
			},
			Expectation: false,
		},
		{
			Name: "same ResourceVersion with both IgnoreSideEffects true",
			A: InputRevisions{
				Key:               "key9",
				ResourceVersion:   "9",
				IgnoreSideEffects: &trueVal,
			},
			B: InputRevisions{
				Key:               "key9",
				ResourceVersion:   "9",
				IgnoreSideEffects: &trueVal,
			},
			Expectation: false,
		},
		{
			Name: "same ResourceVersion with both IgnoreSideEffects false",
			A: InputRevisions{
				Key:               "key10",
				ResourceVersion:   "10",
				IgnoreSideEffects: &falseVal,
			},
			B: InputRevisions{
				Key:               "key10",
				ResourceVersion:   "10",
				IgnoreSideEffects: &falseVal,
			},
			Expectation: false,
		},
		{
			Name: "same ResourceVersion with one nil IgnoreSideEffects",
			A: InputRevisions{
				Key:               "key11",
				ResourceVersion:   "11",
				IgnoreSideEffects: &trueVal,
			},
			B: InputRevisions{
				Key:               "key11",
				ResourceVersion:   "11",
				IgnoreSideEffects: nil,
			},
			Expectation: false,
		},
		{
			Name: "same ResourceVersion with both nil IgnoreSideEffects",
			A: InputRevisions{
				Key:               "key12",
				ResourceVersion:   "12",
				IgnoreSideEffects: nil,
			},
			B: InputRevisions{
				Key:               "key12",
				ResourceVersion:   "12",
				IgnoreSideEffects: nil,
			},
			Expectation: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			if tt.ShouldPanic {
				assert.Panics(t, func() {
					tt.A.Less(tt.B)
				})
				return
			}

			result := tt.A.Less(tt.B)
			assert.Equal(t, tt.Expectation, result)
		})
	}
}

func TestForceSynthesisAnnotation(t *testing.T) {
	comp := &Composition{}
	comp.Status.CurrentSynthesis = &Synthesis{UUID: "123"}

	// Initially false
	assert.False(t, comp.ShouldForceResynthesis())

	// Forcing resynthesis is reflected by ShouldForceResynthesis
	comp.ForceResynthesis()
	assert.True(t, comp.ShouldForceResynthesis())

	// Update the synthesis UUID
	comp.Status.CurrentSynthesis.UUID = "234"
	assert.False(t, comp.ShouldForceResynthesis())
}
