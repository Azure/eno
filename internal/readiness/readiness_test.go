package readiness

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var simpleConditionStatus = map[string]any{
	"status": map[string]any{
		"conditions": []map[string]any{
			{
				"message":            "foo bar",
				"reason":             "Testing",
				"lastTransitionTime": metav1.Now().Format(time.RFC3339),
				"status":             "True",
				"type":               "Test",
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
			{
				"message":            "foo bar",
				"reason":             "Testing",
				"status":             "False",
				"lastTransitionTime": 123,
				"type":               "Test4",
			},
		},
	},
}

var readinessCheckEvalTests = []struct {
	Name          string
	Resource      *unstructured.Unstructured
	Expr          string
	Expect        bool
	ExpectPrecise bool
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
		Expr:     "self.status.conditions.exists(item, item.type == 'Test' && item.status == 'False')",
		Expect:   false,
	},
	{
		Name:     "condition-hit",
		Resource: &unstructured.Unstructured{Object: simpleConditionStatus},
		Expr:     "self.status.conditions.exists(item, item.type == 'Test' && item.status == 'True')",
		Expect:   true,
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
	{
		Name:          "magic-condition-matcher-her",
		Resource:      &unstructured.Unstructured{Object: simpleConditionStatus},
		Expr:          "self.status.conditions.filter(item, item.type == 'Test' && item.status == 'True')",
		Expect:        true,
		ExpectPrecise: true,
	},
	{
		Name:          "magic-condition-matcher-wrong-type",
		Resource:      &unstructured.Unstructured{Object: simpleConditionStatus},
		Expr:          "self.status.conditions.filter(item, item.type == 'Test4')",
		Expect:        false,
		ExpectPrecise: false,
	},
}

func TestReadinessCheckEval(t *testing.T) {
	env, err := NewEnv()
	require.NoError(t, err)

	for _, tc := range readinessCheckEvalTests {
		t.Run(tc.Name, func(t *testing.T) {
			r, err := ParseCheck(env, tc.Expr)
			require.NoError(t, err)

			time, ok := r.Eval(context.Background(), tc.Resource)
			assert.Equal(t, tc.Expect, time != nil)
			assert.Equal(t, time != nil, ok)
			assert.Equal(t, tc.ExpectPrecise, time != nil && time.PreciseTime)

			// Make sure every program can be evaluated multiple times
			time, ok = r.Eval(context.Background(), tc.Resource)
			assert.Equal(t, tc.Expect, time != nil)
			assert.Equal(t, time != nil, ok)
			assert.Equal(t, tc.ExpectPrecise, time != nil && time.PreciseTime)
		})
	}
}

var readinessChecksEvalTests = []struct {
	Name         string
	Checks       ReadinessChecks
	Resource     *unstructured.Unstructured
	ExpectedTime string
}{
	{
		Name:     "empty",
		Checks:   nil,
		Resource: &unstructured.Unstructured{},
	},
	{
		Name: "one-negative",
		Checks: ReadinessChecks{
			mustParse("false"),
		},
		Resource: &unstructured.Unstructured{},
	},
	{
		Name: "one-positive",
		Checks: ReadinessChecks{
			mustParse("true"),
		},
		Resource:     &unstructured.Unstructured{},
		ExpectedTime: time.Now().Format(time.RFC3339),
	},
	{
		Name: "two-positive",
		Checks: ReadinessChecks{
			mustParse("true"),
			mustParse("true"),
		},
		Resource:     &unstructured.Unstructured{},
		ExpectedTime: time.Now().Format(time.RFC3339),
	},
	{
		Name: "one-positive-one-negative",
		Checks: ReadinessChecks{
			mustParse("true"),
			mustParse("false"),
		},
		Resource: &unstructured.Unstructured{},
	},
	{
		Name: "one-positive-condition",
		Checks: ReadinessChecks{
			mustParse("self.conditions.filter(item, item.type == 'Test' && item.status == 'True')"),
		},
		Resource: &unstructured.Unstructured{
			Object: map[string]any{
				"conditions": []map[string]any{
					{
						"message":            "foo bar",
						"reason":             "Testing",
						"lastTransitionTime": time.Now().Add(time.Hour * 12).Format(time.RFC3339),
						"status":             "True",
						"type":               "Test",
					},
				},
			},
		},
		ExpectedTime: time.Now().Add(time.Hour * 12).Format(time.RFC3339),
	},
	{
		Name: "one-low-one-high-precision",
		Checks: ReadinessChecks{
			mustParse("self.conditions.filter(item, item.type == 'Test' && item.status == 'True')"),
			mustParse("true"),
		},
		Resource: &unstructured.Unstructured{
			Object: map[string]any{
				"conditions": []map[string]any{
					{
						"message":            "foo bar",
						"reason":             "Testing",
						"lastTransitionTime": time.Now().Add(-time.Hour * 12).Format(time.RFC3339),
						"status":             "True",
						"type":               "Test",
					},
				},
			},
		},
		// Picks the precise one even though the non-precise one occurs after it
		ExpectedTime: time.Now().Add(-time.Hour * 12).Format(time.RFC3339),
	},
	{
		Name: "two-high-precision",
		Checks: ReadinessChecks{
			mustParse("self.conditions.filter(item, item.type == 'Test' && item.status == 'True')"),
			mustParse("self.conditions.filter(item, item.type == 'Test2' && item.status == 'True')"),
		},
		Resource: &unstructured.Unstructured{
			Object: map[string]any{
				"conditions": []map[string]any{
					{
						"message":            "foo bar",
						"reason":             "Testing",
						"lastTransitionTime": time.Now().Add(-time.Hour * 12).Format(time.RFC3339),
						"status":             "True",
						"type":               "Test",
					},
					{
						"message":            "foo bar",
						"reason":             "Testing",
						"lastTransitionTime": time.Now().Add(time.Hour * 12).Format(time.RFC3339),
						"status":             "True",
						"type":               "Test2",
					},
				},
			},
		},
		// Picks the latest time
		ExpectedTime: time.Now().Add(time.Hour * 12).Format(time.RFC3339),
	},
}

func TestReadinessChecksEval(t *testing.T) {
	for _, tc := range readinessChecksEvalTests {
		t.Run(tc.Name, func(t *testing.T) {
			actual, ok := tc.Checks.Eval(context.Background(), tc.Resource)
			assert.Equal(t, ok, actual != nil)

			if tc.ExpectedTime == "" {
				assert.Nil(t, actual)
			} else {
				exp, err := time.Parse(time.RFC3339, tc.ExpectedTime)
				require.NoError(t, err)
				require.NotNil(t, actual)
				assert.Truef(t, exp.Round(time.Hour*2).Equal(actual.ReadyTime.Round(time.Hour*2)), "actual:%s exp:%s", actual, exp)
			}
		})
	}
}

func mustParse(expr string) *ReadinessCheck {
	e, err := NewEnv()
	if err != nil {
		panic(err)
	}
	check, err := ParseCheck(e, expr)
	if err != nil {
		panic(err)
	}
	return check
}
