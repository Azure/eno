package functiontest

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
		Assertion: func(t *testing.T, scen Scenario[struct{}], outputs []client.Object) {
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

	var calls int
	Evaluate(t, fn, Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: AssertionChain(
			func(t *testing.T, scen Scenario[struct{}], outputs []client.Object) {
				calls++
				t.Logf("assertion 1")
			},
			func(t *testing.T, scen Scenario[struct{}], outputs []client.Object) {
				calls++
				t.Logf("assertion 2")
			},
		),
	})

	assert.Equal(t, 2, calls)
}

func TestLoadScenarios(t *testing.T) {
	fn := func(inputs map[string]any) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Name = "test-pod"
		return []client.Object{output}, nil
	}

	var inputs []map[string]any
	assertion := func(t *testing.T, scen Scenario[map[string]any], outputs []client.Object) {
		inputs = append(inputs, scen.Inputs)
	}

	scenarios := LoadScenarios(t, "fixtures", assertion)
	Evaluate(t, fn, scenarios...)

	assert.Equal(t, []map[string]any{
		{"foo": "bar"},
		{"foo": "baz"},
		{"bar": "baz"},
	}, inputs)
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
