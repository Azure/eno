package functiontest

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	enov1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/pkg/function"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KeyMatchMode int

const (
	KeyMatchStrict  KeyMatchMode = iota // all eno_keys must have a corresponding ref
	KeyMatchRelaxed                     // eno_keys can be a subset of refs
)

// InputsMatchSynthesizerRefs is ensures your synthsizers references stay in sync with the eno_keys to the struct you give function.SynthFunc
// The level of sync can be defined by the KeyMatchMode parameter
func InputsMatchSynthesizerRefs(t require.TestingT, inputObject any, synthesizerPath string, mode KeyMatchMode) {
	enoKeys, err := extractEnoKeys(inputObject)
	require.NoError(t, err, "Failed to extract eno_keys from EnoInputs struct")
	require.NotEmpty(t, enoKeys, "no eno_key tags in input")

	// Load and parse synthesizer.yaml
	synthesizerRefs, err := loadSynthesizerRefs(synthesizerPath)
	require.NoError(t, err, "Failed to load synthizer refs")
	require.NotEmpty(t, synthesizerRefs, "synthesizer.yaml should have refs with keys")

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

// extractEnoKeysWithError extracts eno_key tags from a struct and returns them as a slice of strings.
func extractEnoKeys(structInstance any) ([]string, error) {
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

	return refKeys, nil
}

// ValidateResourceMeta is an Assertion that proves the outputs are valid resources from Eno's perspective
// e.g. contain only valid metadata like eno.azure.io/* annotations.
func ValidateResourceMeta[T function.Inputs]() Assertion[T] {
	return func(t *testing.T, s *Scenario[T], outputs []client.Object) {
		validateResourceMeta(t, outputs)
	}
}

func validateResourceMeta(t require.TestingT, outputs []client.Object) {
	for i, output := range outputs {
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(output)
		if err != nil {
			t.Errorf("resource at index=%d, kind=%s, name=%s could not be converted to unstructured: %s", i, output.GetObjectKind(), output.GetName(), err)
			continue
		}

		_, err = resource.FromUnstructured(&unstructured.Unstructured{Object: obj})
		if err != nil {
			t.Errorf("resource at index=%d, kind=%s, name=%s is invalid: %s", i, output.GetObjectKind(), output.GetName(), err)
		}
	}
}
