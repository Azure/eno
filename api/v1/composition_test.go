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

func TestGetAzureOperationID(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		synthesisEnv []EnvVar
		expected     string
	}{
		{
			name: "operationID from annotations",
			annotations: map[string]string{
				"eno.azure.io/operationID": "annotation-op-123",
			},
			synthesisEnv: []EnvVar{
				{Name: "operationID", Value: "env-op-456"},
			},
			expected: "annotation-op-123", // annotations take precedence
		},
		{
			name:        "operationID from synthesisEnv",
			annotations: map[string]string{},
			synthesisEnv: []EnvVar{
				{Name: "operationID", Value: "env-op-456"},
			},
			expected: "env-op-456",
		},
		{
			name: "operationID missing from both",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			synthesisEnv: []EnvVar{
				{Name: "otherKey", Value: "otherValue"},
			},
			expected: "",
		},
		{
			name: "empty operationID in annotations falls back to synthesisEnv",
			annotations: map[string]string{
				"eno.azure.io/operationID": "",
			},
			synthesisEnv: []EnvVar{
				{Name: "operationID", Value: "env-op-789"},
			},
			expected: "env-op-789",
		},
		{
			name:         "both empty",
			annotations:  map[string]string{},
			synthesisEnv: []EnvVar{},
			expected:     "",
		},
		{
			name:         "nil annotations and synthesisEnv",
			annotations:  nil,
			synthesisEnv: nil,
			expected:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := &Composition{
				Spec: CompositionSpec{
					SynthesisEnv: tt.synthesisEnv,
				},
			}
			comp.Annotations = tt.annotations

			result := comp.GetAzureOperationID()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetAzureOperationOrigin(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		synthesisEnv []EnvVar
		expected     string
	}{
		{
			name: "operationOrigin from annotations",
			annotations: map[string]string{
				"eno.azure.io/operationOrigin": "annotation-origin",
			},
			synthesisEnv: []EnvVar{
				{Name: "operationOrigin", Value: "env-origin"},
			},
			expected: "annotation-origin", // annotations take precedence
		},
		{
			name:        "operationOrigin from synthesisEnv",
			annotations: map[string]string{},
			synthesisEnv: []EnvVar{
				{Name: "operationOrigin", Value: "env-origin"},
			},
			expected: "env-origin",
		},
		{
			name: "operationOrigin missing from both",
			annotations: map[string]string{
				"other-annotation": "value",
			},
			synthesisEnv: []EnvVar{
				{Name: "otherKey", Value: "otherValue"},
			},
			expected: "",
		},
		{
			name: "empty operationOrigin in annotations falls back to synthesisEnv",
			annotations: map[string]string{
				"eno.azure.io/operationOrigin": "",
			},
			synthesisEnv: []EnvVar{
				{Name: "operationOrigin", Value: "env-origin-fallback"},
			},
			expected: "env-origin-fallback",
		},
		{
			name:         "both empty",
			annotations:  map[string]string{},
			synthesisEnv: []EnvVar{},
			expected:     "",
		},
		{
			name:         "nil annotations and synthesisEnv",
			annotations:  nil,
			synthesisEnv: nil,
			expected:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := &Composition{
				Spec: CompositionSpec{
					SynthesisEnv: tt.synthesisEnv,
				},
			}
			comp.Annotations = tt.annotations

			result := comp.GetAzureOperationOrigin()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetSynthesisEnvValue(t *testing.T) {
	tests := []struct {
		name         string
		synthesisEnv []EnvVar
		key          string
		expected     string
	}{
		{
			name: "key exists",
			synthesisEnv: []EnvVar{
				{Name: "key1", Value: "value1"},
				{Name: "key2", Value: "value2"},
			},
			key:      "key2",
			expected: "value2",
		},
		{
			name: "key does not exist",
			synthesisEnv: []EnvVar{
				{Name: "key1", Value: "value1"},
			},
			key:      "key3",
			expected: "",
		},
		{
			name:         "empty synthesisEnv",
			synthesisEnv: []EnvVar{},
			key:          "anyKey",
			expected:     "",
		},
		{
			name:         "nil synthesisEnv",
			synthesisEnv: nil,
			key:          "anyKey",
			expected:     "",
		},
		{
			name: "empty value",
			synthesisEnv: []EnvVar{
				{Name: "emptyKey", Value: ""},
			},
			key:      "emptyKey",
			expected: "",
		},
		{
			name: "duplicate keys - first one wins",
			synthesisEnv: []EnvVar{
				{Name: "dupKey", Value: "first"},
				{Name: "dupKey", Value: "second"},
			},
			key:      "dupKey",
			expected: "first",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &CompositionSpec{
				SynthesisEnv: tt.synthesisEnv,
			}

			result := getSynthesisEnvValue(spec, tt.key)
			assert.Equal(t, tt.expected, result)
		})
	}
}
