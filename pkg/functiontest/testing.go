package functiontest

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/internal/resource"
	"github.com/Azure/eno/pkg/function"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Scenario represents a test case for a synthesizer function.
type Scenario[T function.Inputs] struct {
	Name      string
	Inputs    T
	Assertion Assertion[T]
}

// Evaluate runs the synthesizer function with the provided scenarios and asserts on the outputs.
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

func WithOverrides[T function.Inputs](next function.SynthFunc[T], args ...any) function.SynthFunc[T] {
	return func(inputs T) ([]client.Object, error) {
		outputs, err := next(inputs)
		if err != nil {
			return nil, err
		}
		return applyOverrides(args, outputs)
	}
}

func applyOverrides(options []any, outputs []client.Object) ([]client.Object, error) {
	comp := &apiv1.Composition{}
	for _, opt := range options {
		if c, ok := opt.(*apiv1.Composition); ok {
			comp = c
		}
	}

	copy := []client.Object{}
	for _, output := range outputs {
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(output)
		if err != nil {
			return nil, err
		}
		u := &unstructured.Unstructured{Object: obj}

		res, err := resource.FromUnstructured(u)
		if err != nil {
			return nil, err
		}

		// Find the current state of this object if specified by the options
		current := u
		for _, opt := range options {
			c, ok := opt.(client.Object)
			if !ok || c.GetObjectKind().GroupVersionKind() != u.GroupVersionKind() || c.GetName() != u.GetName() || c.GetNamespace() != u.GetNamespace() {
				continue
			}
			obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(c)
			if err != nil {
				return nil, err
			}
			current = &unstructured.Unstructured{Object: obj}
			continue
		}

		snap, err := res.Snapshot(context.Background(), comp, current)
		if err != nil {
			return nil, err
		}

		structCopy := output.DeepCopyObject()
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(snap.Unstructured().Object, structCopy)
		if err != nil {
			return nil, err
		}
		copy = append(copy, structCopy.(client.Object))
	}

	return copy, nil
}
