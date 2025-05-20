package functiontest

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Azure/eno/pkg/function"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Scenario represents a test case for a synthesizer function.
// It contains the name of the scenario, the inputs to provide to the function,
// and an assertion function to validate the outputs.
type Scenario[T function.Inputs] struct {
	Name      string       // Name identifies the test scenario
	Inputs    T            // Inputs to provide to the function
	Assertion Assertion[T] // Function to validate the outputs
}

// Evaluate runs the synthesizer function with the provided scenarios and asserts on the outputs.
// Each scenario is run as a separate subtest, and the function's outputs are passed to the scenario's
// assertion function for validation.
func Evaluate[T function.Inputs](t *testing.T, synth function.SynthFunc[T], scenarios ...Scenario[T]) {
	for _, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			outputs, err := synth(s.Inputs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			s.Assertion(t, &s, outputs)
		})
	}
}

// LoadScenarios recursively loads yaml and json input fixtures from the specified directory.
// Each file is loaded as a scenario with the given assertion function.
// The scenarios are shuffled to ensure tests aren't dependent on execution order.
//
// Example:
//
//	func TestMyFunction(t *testing.T) {
//	    fn := func(inputs MyInputs) ([]client.Object, error) {
//	        // Your function implementation
//	    }
//
//	    scenarios := functiontest.LoadScenarios(t, "testdata/fixtures", func(t *testing.T, s *functiontest.Scenario[MyInputs], outputs []client.Object) {
//	        // Assertions on outputs
//	    })
//	    functiontest.Evaluate(t, fn, scenarios...)
//	}
func LoadScenarios[T any](t *testing.T, dir string, assertion Assertion[T]) []Scenario[T] {
	scenarios := []Scenario[T]{}
	walkFiles(t, dir, func(path, name string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("error while reading fixture %q: %s", path, err)
			return nil
		}

		var input T
		err = yaml.Unmarshal(data, &input)
		if err != nil {
			t.Errorf("error while parsing fixture %q: %s", path, err)
			return nil
		}
		scenarios = append(scenarios, Scenario[T]{
			Name:      name,
			Inputs:    input,
			Assertion: assertion,
		})
		return nil
	})

	// Make sure tests aren't coupled to a particular execution order
	rand.Shuffle(len(scenarios), func(i, j int) { scenarios[i], scenarios[j] = scenarios[j], scenarios[i] })

	return scenarios
}

// Assertion is a function that validates the outputs of a synthesizer function.
// It receives the testing.T instance, the scenario that was run, and the outputs from the function.
type Assertion[T function.Inputs] func(t *testing.T, s *Scenario[T], outputs []client.Object)

// AssertionChain is a helper function to create an assertion that runs multiple assertions in sequence.
// This allows composing multiple assertions for the same scenario.
//
// Example:
//
//	func TestMyFunction(t *testing.T) {
//	    fn := func(inputs struct{}) ([]client.Object, error) {
//	        // Your function implementation
//	    }
//
//	    hasOnePod := func(t *testing.T, s *functiontest.Scenario[struct{}], outputs []client.Object) {
//	        require.Len(t, outputs, 1)
//	        _, ok := outputs[0].(*corev1.Pod)
//	        require.True(t, ok)
//	    }
//
//	    podHasName := func(t *testing.T, s *functiontest.Scenario[struct{}], outputs []client.Object) {
//	        pod := outputs[0].(*corev1.Pod)
//	        assert.Equal(t, "test-pod", pod.Name)
//	    }
//
//	    functiontest.Evaluate(t, fn, functiontest.Scenario[struct{}]{
//	        Name:      "creates-pod",
//	        Inputs:    struct{}{},
//	        Assertion: functiontest.AssertionChain(hasOnePod, podHasName),
//	    })
//	}
func AssertionChain[T function.Inputs](asserts ...Assertion[T]) Assertion[T] {
	return func(t *testing.T, s *Scenario[T], outputs []client.Object) {
		for i, assert := range asserts {
			t.Run(fmt.Sprintf("assertion-%d", i), func(t *testing.T) {
				assert(t, s, outputs)
			})
		}
	}
}

// LoadSnapshots returns an assertion that will compare the outputs of a synthesizer function
// with the expected outputs stored in snapshot files.
//
// Scenarios that do not have a corresponding snapshot file will be ignored.
// To generate snapshots, set the ENO_GEN_SNAPSHOTS environment variable to a non-empty value.
//
// So, to bootstrap snapshots for a given fixture/scenario: create an empty snapshot file
// that matches the name of the scenario (or fixture if using LoadScenarios), and run the
// tests with ENO_GEN_SNAPSHOTS=true.
//
// Example:
//
//	func TestMyFunction(t *testing.T) {
//	    fn := func(inputs MyInputs) ([]client.Object, error) {
//	        // Your function implementation
//	    }
//
//	    assertion := functiontest.LoadSnapshots[MyInputs](t, "testdata/snapshots")
//	    scenarios := functiontest.LoadScenarios(t, "testdata/fixtures", assertion)
//	    functiontest.Evaluate(t, fn, scenarios...)
//	}
func LoadSnapshots[T function.Inputs](t *testing.T, dir string) Assertion[T] {
	snapshots := map[string][]byte{}
	walkFiles(t, dir, func(path, name string) error {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("error while reading fixture %q: %s", path, err)
			return nil
		}
		snapshots[name] = data
		return nil
	})

	return func(t *testing.T, s *Scenario[T], outputs []client.Object) {
		expected, ok := snapshots[s.Name]
		if !ok {
			return
		}

		data, err := yaml.Marshal(&outputs)
		if err != nil {
			t.Errorf("error while marshalling outputs: %s", err)
		}

		if os.Getenv("ENO_GEN_SNAPSHOTS") != "" {
			err = os.WriteFile(filepath.Join(dir, s.Name+".yaml"), data, 0644)
			if err != nil {
				t.Errorf("error while writing snapshot %q: %s", s.Name, err)
			}
			return
		}

		if !bytes.Equal(data, expected) {
			t.Errorf("outputs do not match the snapshot - re-run tests with ENO_GEN_SNAPSHOTS=true to update them")
		}
	}
}

func walkFiles(t *testing.T, dir string, fn func(path, name string) error) {
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		ext := filepath.Ext(info.Name())
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			return nil
		}
		return fn(path, info.Name()[:len(info.Name())-len(ext)])
	})
	if err != nil {
		t.Errorf("error while walking files: %s", err)
	}
}
