package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInputRevisionsLess(t *testing.T) {
	revision1 := 1
	revision2 := 2
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
