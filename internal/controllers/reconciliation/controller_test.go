package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Azure/eno/internal/reconstitution"
)

func TestMungePatch(t *testing.T) {
	patch, err := mungePatch([]byte(`{"metadata":{"creationTimestamp":"2024-03-05T00:45:27Z"}, "foo":"bar"}`), "test-rv")
	require.NoError(t, err)
	assert.JSONEq(t, `{"metadata":{"resourceVersion":"test-rv"},"foo":"bar"}`, string(patch))
}

func TestMungePatchEmpty(t *testing.T) {
	patch, err := mungePatch([]byte(`{"metadata":{"creationTimestamp":"2024-03-05T00:45:27Z"}}`), "test-rv")
	require.NoError(t, err)
	assert.Nil(t, patch)
}

var evalReadinessChecksTests = []struct {
	Name         string
	Checks       []*reconstitution.ReadinessCheck
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
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("false"),
		},
		Resource: &unstructured.Unstructured{},
	},
	{
		Name: "one-positive",
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("true"),
		},
		Resource:     &unstructured.Unstructured{},
		ExpectedTime: time.Now().Format(time.RFC3339),
	},
	{
		Name: "two-positive",
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("true"),
			reconstitution.MustReadinessCheckTest("true"),
		},
		Resource:     &unstructured.Unstructured{},
		ExpectedTime: time.Now().Format(time.RFC3339),
	},
	{
		Name: "one-positive-one-negative",
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("true"),
			reconstitution.MustReadinessCheckTest("false"),
		},
		Resource: &unstructured.Unstructured{},
	},
	{
		Name: "one-positive-condition",
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("self.conditions.filter(item, item.type == 'Test' && item.status == 'True')"),
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
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("self.conditions.filter(item, item.type == 'Test' && item.status == 'True')"),
			reconstitution.MustReadinessCheckTest("true"),
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
		Checks: []*reconstitution.ReadinessCheck{
			reconstitution.MustReadinessCheckTest("self.conditions.filter(item, item.type == 'Test' && item.status == 'True')"),
			reconstitution.MustReadinessCheckTest("self.conditions.filter(item, item.type == 'Test2' && item.status == 'True')"),
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

func TestEvalReadinessChecks(t *testing.T) {
	for _, tc := range evalReadinessChecksTests {
		t.Run(tc.Name, func(t *testing.T) {
			resource := &reconstitution.Resource{}
			resource.ReadinessChecks = tc.Checks
			actual := evalReadinessChecks(context.Background(), resource, tc.Resource)

			if tc.ExpectedTime == "" {
				assert.Nil(t, actual)
			} else {
				exp, err := time.Parse(time.RFC3339, tc.ExpectedTime)
				require.NoError(t, err)
				require.NotNil(t, actual)
				assert.Truef(t, exp.Round(time.Hour*2).Equal(actual.Round(time.Hour*2)), "actual:%s exp:%s", actual, exp)
			}
		})
	}
}
