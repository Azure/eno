package functiontest

import (
	"sync"
	"sync/atomic"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
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

func TestWithOverride_NoCondition(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Kind = "Pod"
		output.APIVersion = "v1" // TODO: WithScheme
		output.Name = "test-pod"
		output.Annotations = map[string]string{
			"eno.azure.io/overrides": `[{ "path": "self.spec.containers[name='test'].image", "value": "overridden-image" }]`,
		}
		output.Spec.Containers = []corev1.Container{{
			Name:  "test",
			Image: "original-image",
		}}
		return []client.Object{output}, nil
	}

	Evaluate(t, WithOverrides(fn), Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: func(t *testing.T, s *Scenario[struct{}], outputs []client.Object) {
			assert.Equal(t, "overridden-image", outputs[0].(*corev1.Pod).Spec.Containers[0].Image)
		},
	})
}

func TestWithOverride_MatchCurrentState(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Kind = "Pod"
		output.APIVersion = "v1" // TODO: WithScheme
		output.Name = "test-pod"
		output.Annotations = map[string]string{
			"eno.azure.io/overrides": `[{ "path": "self.spec.containers[name='test'].image", "value": "overridden-image", "condition": "self.metadata.annotations['foo'] == 'bar'" }]`,
			"foo":                    "not-bar",
		}
		output.Spec.Containers = []corev1.Container{{
			Name:  "test",
			Image: "original-image",
		}}
		return []client.Object{output}, nil
	}

	other := &corev1.Pod{}
	other.Kind = "Pod"
	other.APIVersion = "v1"
	other.Name = "another-pod"
	other.Annotations = map[string]string{"foo": "baz"}

	current := &corev1.Pod{}
	current.Kind = "Pod"
	current.APIVersion = "v1"
	current.Name = "test-pod"
	current.Annotations = map[string]string{"foo": "bar"}

	Evaluate(t, WithOverrides(fn, current, other), Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: func(t *testing.T, s *Scenario[struct{}], outputs []client.Object) {
			assert.Equal(t, "overridden-image", outputs[0].(*corev1.Pod).Spec.Containers[0].Image)
		},
	})
}

func TestWithOverride_MatchComposition(t *testing.T) {
	fn := func(inputs struct{}) ([]client.Object, error) {
		output := &corev1.Pod{}
		output.Kind = "Pod"
		output.APIVersion = "v1" // TODO: WithScheme
		output.Name = "test-pod"
		output.Annotations = map[string]string{
			"eno.azure.io/overrides": `[{ "path": "self.spec.containers[name='test'].image", "value": "overridden-image", "condition": "composition.metadata.annotations['foo'] == 'bar'" }]`,
		}
		output.Spec.Containers = []corev1.Container{{
			Name:  "test",
			Image: "original-image",
		}}
		return []client.Object{output}, nil
	}

	comp := &apiv1.Composition{}
	comp.Annotations = map[string]string{"foo": "bar"}

	Evaluate(t, WithOverrides(fn, comp), Scenario[struct{}]{
		Name:   "example-test",
		Inputs: struct{}{},
		Assertion: func(t *testing.T, s *Scenario[struct{}], outputs []client.Object) {
			assert.Equal(t, "overridden-image", outputs[0].(*corev1.Pod).Spec.Containers[0].Image)
		},
	})
}

// TODO: helpers to generate metadata.managedFields so folks don't have to manually encode json to test pathOwnedByEno
