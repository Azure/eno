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

func TestInputsMatchSynthesizerRefs(t *testing.T) {
	tests := []struct {
		name     string
		inputs   interface{}
		path     string
		mode     KeyMatchMode
		wantFail bool
		wantErr  string
	}{
		{"StrictMode_Success", TestInputsComplete{}, "lintfixtures/synthesizer_complete.yaml", KeyMatchStrict, false, ""},
		{"StrictMode_FailureMissingEnoKey", TestInputsPartial{}, "lintfixtures/synthesizer_extended.yaml", KeyMatchStrict, true, "network"},
		{"StrictMode_FailureExtraEnoKey", TestInputsComplete{}, "lintfixtures/synthesizer_partial.yaml", KeyMatchStrict, true, "storage"},
		{"RelaxedMode_Success", TestInputsPartial{}, "lintfixtures/synthesizer_extended.yaml", KeyMatchRelaxed, false, ""},
		{"RelaxedMode_FailureMissingRef", TestInputsComplete{}, "lintfixtures/synthesizer_partial.yaml", KeyMatchRelaxed, true, "storage"},
		{"EmptyEnoKeys", TestInputsEmpty{}, "lintfixtures/synthesizer_partial.yaml", KeyMatchStrict, true, "no eno_key tags in input"},
		{"InvalidSynthesizerPath", TestInputsComplete{}, "/tmp/non-existent-synthesizer.yaml", KeyMatchStrict, true, "Failed to load synthizer refs"},
		{"InvalidSynthesizerYaml", TestInputsComplete{}, "lintfixtures/synthesizer_invalid.yaml", KeyMatchStrict, true, "Failed to load synthizer refs"},
		{"SynthesizerWithoutRefs", TestInputsComplete{}, "lintfixtures/synthesizer_empty.yaml", KeyMatchStrict, true, "synthesizer.yaml should have refs with keys"},
		{"PointerInput", &TestInputsComplete{}, "lintfixtures/synthesizer_complete.yaml", KeyMatchStrict, false, ""},
		{"NonStructInput", "not a struct", "lintfixtures/synthesizer_partial.yaml", KeyMatchStrict, true, "Failed to extract eno_keys"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantFail {
				mockT := &mockTestingT{}
				InputsMatchSynthesizerRefs(mockT, tc.inputs, tc.path, tc.mode)
				assert.True(t, mockT.failed, "Expected failure for %s", tc.name)
				assert.Contains(t, mockT.errorMsg, tc.wantErr, "Unexpected error message for %s", tc.name)
			} else {
				InputsMatchSynthesizerRefs(t, tc.inputs, tc.path, tc.mode)
			}
		})
	}
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
