package functiontest

import (
	"sync"
	"sync/atomic"
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
			if len(outputs) != 1 {
				t.Errorf("expected 1 output, got %d", len(outputs))
			}
		},
	})
}

func TestAssertionChain(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	calls := atomic.Int64{}
	Evaluate(t, fn, Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: AssertionChain(
			func(t *testing.T, scen *Scenario[struct{}], outputs []client.Object) {
				calls.Add(1)
			},
			func(t *testing.T, scen *Scenario[struct{}], outputs []client.Object) {
				calls.Add(1)
			},
		),
	})

	if calls.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", calls.Load())
	}
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

	if len(inputs) != 3 {
		t.Fatalf("expected 3 inputs, got %d", len(inputs))
	}
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

func TestEvaluateValidateResourceMeta(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.APIVersion = "v1"
		output.Kind = "Pod"
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	Evaluate(t, fn, Scenario[struct{}]{
		Name:      "example-test",
		Inputs:    struct{}{},
		Assertion: ValidateResourceMeta[struct{}](),
	})
}
