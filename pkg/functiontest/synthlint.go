package functiontest

import (
	"fmt"
	"os"
	"reflect"

	enov1 "github.com/Azure/eno/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Define a new type for your matching mode instead of using a bare bool or int
type KeyMatchMode int

const (
	KeyMatchStrict  KeyMatchMode = iota // all eno_keys must have a corresponding ref
	KeyMatchRelaxed                     // eno_keys can be a subset of refs
)

func InputsMatchSynthesizerRefs(t require.TestingT, inputObject any, synthesizerPath string, mode KeyMatchMode) {
	enoKeys, err := extractEnoKeysWithError(inputObject)
	require.NoError(t, err, "Failed to extract eno_keys from EnoInputs struct")

	// Load and parse synthesizer.yaml
	synthesizerRefs, err := loadSynthesizerRefs(synthesizerPath)
	require.NoError(t, err, "Failed to load synthizer refs")

	// Verify that every eno_key has a corresponding ref in synthesizer.yaml
	for _, enoKey := range enoKeys {
		assert.Contains(t, synthesizerRefs, enoKey,
			"eno_key '%s' from EnoInputs struct must have a corresponding ref in synthesizer.yaml", enoKey)
	}

	// Optional: Log info about refs that don't have corresponding eno_keys
	// (these might be intentional for other purposes)
	if mode == KeyMatchStrict {
		for _, ref := range synthesizerRefs {
			assert.Contains(t, enoKeys, ref)
		}
	}
}

// extractEnoKeysWithError is a version that returns an error instead of failing the test
func extractEnoKeysWithError(structInstance any) ([]string, error) {
	var enoKeys []string

	// Get the type of the provided struct
	structType := reflect.TypeOf(structInstance)

	// Handle pointer types by getting the element type
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}

	// Ensure we're working with a struct
	if structType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("extractEnoKeys requires a struct type, got %v", structType.Kind())
	}

	// Iterate through all fields
	for i := range structType.NumField() {
		field := structType.Field(i)

		// Check if field has eno_key tag
		if enoKey, ok := field.Tag.Lookup("eno_key"); ok {
			enoKeys = append(enoKeys, enoKey)
		}
	}

	if len(enoKeys) == 0 {
		return nil, fmt.Errorf("no eno_key tags in input")
	}

	return enoKeys, nil
}

// loadSynthesizerRefs loads the synthesizer.yaml file and extracts all ref keys
func loadSynthesizerRefs(synthesizerPath string) ([]string, error) {

	// Read the YAML file
	data, err := os.ReadFile(synthesizerPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read synthesizer.yaml file at %s: %w", synthesizerPath, err)
	}

	// Parse the YAML using the eno Synthesizer type
	var synthesizer enov1.Synthesizer
	if err := yaml.Unmarshal(data, &synthesizer); err != nil {
		return nil, fmt.Errorf("failed to parse synthesizer.yaml: %w", err)
	}

	// Extract ref keys
	var refKeys []string
	for _, ref := range synthesizer.Spec.Refs {
		if ref.Key != "" {
			refKeys = append(refKeys, ref.Key)
		}
	}

	if len(refKeys) == 0 {
		return nil, fmt.Errorf("synthesizer.yaml should have refs with keys")
	}
	return refKeys, nil
}
