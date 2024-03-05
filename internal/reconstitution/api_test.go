package reconstitution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var simpleConditionStatus = map[string]any{
	"status": map[string]any{
		"conditions": []map[string]any{
			{
				"message": "foo bar",
				"reason":  "Testing",
				"status":  "True",
				"type":    "Test",
			},
			{
				"message": "foo bar",
				"reason":  "Testing",
				"status":  "True",
				"type":    "Test2",
			},
			{
				"message": "foo bar",
				"reason":  "Testing",
				"status":  "False",
				"type":    "Test3",
			},
		},
	},
}

var readinessEvalTests = []struct {
	Name     string
	Resource *unstructured.Unstructured
	Expr     string
	Expect   bool
}{
	{
		Name:     "empty",
		Resource: nil,
		Expr:     "self",
		Expect:   false,
	},
	{
		Name: "simple-miss",
		Resource: &unstructured.Unstructured{
			Object: map[string]any{"foo": "bar"},
		},
		Expr:   "self.foo == 'baz'",
		Expect: false,
	},
	{
		Name: "simple-hit",
		Resource: &unstructured.Unstructured{
			Object: map[string]any{"foo": "bar"},
		},
		Expr:   "self.foo == 'bar'",
		Expect: true,
	},
	{
		Name:     "condition-miss",
		Resource: &unstructured.Unstructured{Object: simpleConditionStatus},
		Expr:     "self.status.conditions.exists(item, item.type == 'Test' && item.status == 'True')",
		Expect:   true,
	},
	{
		Name:     "condition-hit",
		Resource: &unstructured.Unstructured{Object: simpleConditionStatus},
		Expr:     "self.status.conditions.exists(item, item.type == 'Test' && item.status == 'False')",
		Expect:   false,
	},
	{
		Name:     "condition-missing",
		Resource: &unstructured.Unstructured{Object: simpleConditionStatus},
		Expr:     "self.status.conditions.exists(item, item.type == 'TestFoo' && item.status == 'True')",
		Expect:   false,
	},
	{
		Name: "all-conditions-missing",
		Resource: &unstructured.Unstructured{
			Object: map[string]any{
				"status": map[string]any{},
			},
		},
		Expr:   "self.status.conditions.exists(item, item.type == 'TestFoo' && item.status == 'True')",
		Expect: false,
	},
}

func TestReadinessEval(t *testing.T) {
	env, err := newCelEnv()
	require.NoError(t, err)

	for _, tc := range readinessEvalTests {
		t.Run(tc.Name, func(t *testing.T) {
			r, err := newReadinessCheck(env, tc.Expr)
			require.NoError(t, err)

			ok := r.Eval(context.Background(), tc.Resource)
			assert.Equal(t, tc.Expect, ok)
		})
	}
}
