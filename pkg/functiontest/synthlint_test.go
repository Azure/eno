package functiontest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test structures with eno_key tags
type TestInputsComplete struct {
	Database string `eno_key:"database"`
	Cache    string `eno_key:"cache"`
	Storage  string `eno_key:"storage"`
}

type TestInputsPartial struct {
	Database string `eno_key:"database"`
	Cache    string `eno_key:"cache"`
}

type TestInputsExtra struct {
	Database string `eno_key:"database"`
	Cache    string `eno_key:"cache"`
	Storage  string `eno_key:"storage"`
	Network  string `eno_key:"network"`
}

type TestInputsEmpty struct {
	Field1 string `json:"field1"`
	Field2 string `json:"field2"`
}

func TestInputsMatchSynthesizerRefs_StrictMode_Success(t *testing.T) {
	synthesizerPath := "lintfixtures/synthesizer_complete.yaml"

	// Test with exact match - should pass
	inputs := TestInputsComplete{}

	// This should not fail in strict mode since all keys match exactly
	InputsMatchSynthesizerRefs(t, inputs, synthesizerPath, KeyMatchStrict)
}

func TestInputsMatchSynthesizerRefs_StrictMode_FailureMissingEnoKey(t *testing.T) {
	// Use fixture file with more refs than eno_keys
	synthesizerPath := "lintfixtures/synthesizer_extended.yaml"

	// Test with missing eno_key - should fail in strict mode
	inputs := TestInputsPartial{} // only has database and cache

	// Create a mock testing.T to capture the failure
	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchStrict)

	// Verify that the test failed
	assert.True(t, mockT.failed, "Expected test to fail in strict mode when eno_keys are missing")
	assert.Contains(t, mockT.errorMsg, "network", "Expected error message to mention missing 'network' key")
}

func TestInputsMatchSynthesizerRefs_StrictMode_FailureExtraEnoKey(t *testing.T) {
	// Use fixture file with fewer refs than eno_keys
	synthesizerPath := "lintfixtures/synthesizer_partial.yaml"

	// Test with extra eno_key - should fail in strict mode
	inputs := TestInputsComplete{} // has database, cache, and storage

	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchStrict)

	// Verify that the test failed
	assert.True(t, mockT.failed, "Expected test to fail in strict mode when extra eno_keys exist")
	assert.Contains(t, mockT.errorMsg, "storage", "Expected error message to mention extra 'storage' key")
}

func TestInputsMatchSynthesizerRefs_RelaxedMode_Success(t *testing.T) {
	// Use fixture file with more refs than eno_keys
	synthesizerPath := "lintfixtures/synthesizer_extended.yaml"

	// Test with subset of refs - should pass in relaxed mode
	inputs := TestInputsPartial{} // only has database and cache

	// This should not fail in relaxed mode since eno_keys are a subset of refs
	InputsMatchSynthesizerRefs(t, inputs, synthesizerPath, KeyMatchRelaxed)
}

func TestInputsMatchSynthesizerRefs_RelaxedMode_FailureMissingRef(t *testing.T) {
	// Use fixture file with fewer refs than eno_keys
	synthesizerPath := "lintfixtures/synthesizer_partial.yaml"

	// Test with eno_key not in refs - should fail even in relaxed mode
	inputs := TestInputsComplete{} // has database, cache, and storage

	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchRelaxed)

	// Verify that the test failed
	assert.True(t, mockT.failed, "Expected test to fail in relaxed mode when eno_key has no corresponding ref")
	assert.Contains(t, mockT.errorMsg, "storage", "Expected error message to mention 'storage' key without corresponding ref")
}

func TestInputsMatchSynthesizerRefs_EmptyEnoKeys(t *testing.T) {
	// Use fixture file with some refs
	synthesizerPath := "lintfixtures/synthesizer_partial.yaml"

	// Test with struct that has no eno_key tags
	inputs := TestInputsEmpty{}

	mockT := &mockTestingT{}
	// Should pass in both modes since there are no eno_keys to validate
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchStrict)
	assert.True(t, mockT.failed, "Expected test to fail when no eno_key tags exist")
	assert.Contains(t, mockT.errorMsg, "no eno_key tags in input", "Expected error about no eno_key tags")
}

func TestInputsMatchSynthesizerRefs_InvalidSynthesizerPath(t *testing.T) {
	inputs := TestInputsComplete{}
	nonExistentPath := "/tmp/non-existent-synthesizer.yaml"

	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, nonExistentPath, KeyMatchStrict)

	// Verify that the test failed due to file not found
	assert.True(t, mockT.failed, "Expected test to fail when synthesizer file doesn't exist")
	assert.Contains(t, mockT.errorMsg, "Failed to load synthizer refs", "Expected error about loading synthesizer refs")
}

func TestInputsMatchSynthesizerRefs_InvalidSynthesizerYaml(t *testing.T) {
	// Use fixture file with invalid YAML
	synthesizerPath := "lintfixtures/synthesizer_invalid.yaml"

	inputs := TestInputsComplete{}

	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchStrict)

	// Verify that the test failed due to invalid YAML
	assert.True(t, mockT.failed, "Expected test to fail when synthesizer YAML is invalid")
	assert.Contains(t, mockT.errorMsg, "Failed to load synthizer refs", "Expected error about loading synthesizer refs")
}

func TestInputsMatchSynthesizerRefs_SynthesizerWithoutRefs(t *testing.T) {
	// Use fixture file without refs
	synthesizerPath := "lintfixtures/synthesizer_empty.yaml"

	inputs := TestInputsComplete{}

	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchStrict)

	// Verify that the test failed due to missing refs
	assert.True(t, mockT.failed, "Expected test to fail when synthesizer has no refs")
	assert.Contains(t, mockT.errorMsg, "synthesizer.yaml should have refs with keys", "Expected error about missing refs")
}

func TestInputsMatchSynthesizerRefs_PointerInput(t *testing.T) {
	// Test with pointer to struct
	synthesizerPath := "lintfixtures/synthesizer_complete.yaml"

	inputs := &TestInputsComplete{}

	// Should work with pointer to struct
	InputsMatchSynthesizerRefs(t, inputs, synthesizerPath, KeyMatchStrict)
}

func TestInputsMatchSynthesizerRefs_NonStructInput(t *testing.T) {
	synthesizerPath := "lintfixtures/synthesizer_partial.yaml"

	// Test with non-struct input
	inputs := "not a struct"

	mockT := &mockTestingT{}
	InputsMatchSynthesizerRefs(mockT, inputs, synthesizerPath, KeyMatchStrict)

	// Verify that the test failed due to non-struct input
	assert.True(t, mockT.failed, "Expected test to fail when input is not a struct")
	assert.Contains(t, mockT.errorMsg, "Failed to extract eno_keys", "Expected error about extracting eno_keys")
}

// Mock implementation of require.TestingT to capture test failures
type mockTestingT struct {
	failed   bool
	errorMsg string
}

func (m *mockTestingT) Errorf(format string, args ...interface{}) {
	m.failed = true
	if m.errorMsg == "" {
		m.errorMsg = fmt.Sprintf(format, args...)
	}
}

func (m *mockTestingT) FailNow() {
	m.failed = true
}

func (m *mockTestingT) Helper() {}
