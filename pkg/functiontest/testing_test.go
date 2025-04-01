package functiontest

import (
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestEvaluateBasics(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	Evaluate(t, fn, Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: func(t *testing.T, scen *Scenario[struct{}], outputs []client.Object) {
			t.Logf("asserting on %d outputs", len(outputs))
		},
	})
}

func TestAssertionChain(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	Evaluate(t, fn, Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: AssertionChain(
			func(t *testing.T, scen *Scenario[struct{}], outputs []client.Object) {
				t.Logf("assertion 1")
			},
			func(t *testing.T, scen *Scenario[struct{}], outputs []client.Object) {
				t.Logf("assertion 2")
			},
		),
	})
}

func TestLoadScenarios(t *testing.T) {
	fn := func(inputs map[string]any) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	var inputs []map[string]any
	var lock sync.Mutex
	assertion := func(t *testing.T, scen *Scenario[map[string]any], outputs []client.Object) {
		lock.Lock()
		inputs = append(inputs, scen.Inputs)
		lock.Unlock()
	}

	scenarios := LoadScenarios(t, "fixtures", assertion)
	Evaluate(t, fn, scenarios...)
}

func TestLoadSnapshots(t *testing.T) {
	fn := func(inputs map[string]any) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	assertion := LoadSnapshots[map[string]any](t, "snapshots")
	scenarios := LoadScenarios(t, "fixtures", assertion)
	Evaluate(t, fn, scenarios...)
}
