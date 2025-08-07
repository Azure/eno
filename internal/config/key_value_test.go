package config

import (
	"reflect"
	"testing"
)

func TestParseKeyValue(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectedKey string
		expectedVal string
	}{
		{
			name:        "key with value",
			input:       "key=value",
			expectedKey: "key",
			expectedVal: "value",
		},
		{
			name:        "key without value",
			input:       "key",
			expectedKey: "key",
			expectedVal: "",
		},
		{
			name:        "key with empty value",
			input:       "key=",
			expectedKey: "key",
			expectedVal: "",
		},
		{
			name:        "value with equals sign",
			input:       "key=value=extra",
			expectedKey: "key",
			expectedVal: "value=extra",
		},
		{
			name:        "empty input",
			input:       "",
			expectedKey: "",
			expectedVal: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, val := ParseKeyValue(tt.input)
			if key != tt.expectedKey {
				t.Errorf("ParseKeyValue(%q) key = %q, expected %q", tt.input, key, tt.expectedKey)
			}
			if val != tt.expectedVal {
				t.Errorf("ParseKeyValue(%q) val = %q, expected %q", tt.input, val, tt.expectedVal)
			}
		})
	}
}

func TestParseKeyValuePairs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:     "single key-value pair",
			input:    "key1=value1",
			expected: map[string]string{"key1": "value1"},
		},
		{
			name:     "multiple key-value pairs",
			input:    "key1=value1,key2=value2,key3=value3",
			expected: map[string]string{"key1": "value1", "key2": "value2", "key3": "value3"},
		},
		{
			name:     "key without value",
			input:    "key1,key2=value2",
			expected: map[string]string{"key1": "", "key2": "value2"},
		},
		{
			name:     "spaces around pairs",
			input:    " key1=value1 , key2=value2 ",
			expected: map[string]string{"key1": "value1", "key2": "value2"},
		},
		{
			name:     "empty pairs ignored",
			input:    "key1=value1,,key2=value2,",
			expected: map[string]string{"key1": "value1", "key2": "value2"},
		},
		{
			name:     "value with equals sign",
			input:    "key1=value1=extra",
			expected: map[string]string{"key1": "value1=extra"},
		},
		{
			name:     "key with empty value explicit",
			input:    "key1=,key2=value2",
			expected: map[string]string{"key1": "", "key2": "value2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseKeyValuePairs(tt.input)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ParseKeyValuePairs(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}