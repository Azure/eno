package functiontest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/Azure/eno/pkg/function"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Scenario represents a test case for a synthesizer function.
type Scenario[T function.Inputs] struct {
	Name        string
	Inputs      T
	Assertion   Assertion[T]
	ExpectError bool
}

func (s *Scenario[T]) Evaluate(t *testing.T, synth function.SynthFunc[T]) {
	outputs, err := synth(s.Inputs)
	if err != nil && !s.ExpectError {
		t.Fatalf("unexpected error: %v", err)
	} else if err == nil && s.ExpectError {
		t.Fatal("expected error, got nil")
	}
	s.Assertion(t, s, outputs)
}

// Evaluate runs the synthesizer function with the provided scenarios and asserts on the outputs.
func Evaluate[T function.Inputs](t *testing.T, synth function.SynthFunc[T], scenarios ...Scenario[T]) {
	for _, s := range scenarios {
		t.Run(s.Name, func(t *testing.T) {
			t.Parallel()
			s.Evaluate(t, synth)
		})
	}
}

// LoadScenarios recursively loads yaml and json input fixtures from the specified directory.
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
	return scenarios
}

type Assertion[T function.Inputs] func(t *testing.T, s *Scenario[T], outputs []client.Object)

// AssertionChain is a helper function to create an assertion that runs multiple assertions in sequence.
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
