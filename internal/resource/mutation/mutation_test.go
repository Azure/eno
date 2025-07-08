package mutation

import (
	"context"
	"fmt"
	"testing"

	enocel "github.com/Azure/eno/internal/cel"
	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApply(t *testing.T) {
	testCases := []struct {
		name                  string
		path                  string
		obj                   map[string]any
		value                 any
		expected              map[string]any
		wantErr, wantParseErr bool
	}{
		{
			name:     "Map_TopLevel",
			path:     "self.foo",
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{"foo": 123},
		},
		{
			name:     "Map_Nested",
			path:     "self.foo.bar",
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"bar": 123}, "another": "baz"},
		},
		{
			name:     "Map_NestedNil",
			path:     "self.foo.bar",
			obj:      map[string]any{"foo": nil, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": nil, "another": "baz"},
		},
		{
			name:     "Map_NestedMissing",
			path:     "self.foo.bar",
			obj:      map[string]any{"another": "baz"},
			value:    123,
			expected: map[string]any{"another": "baz"},
		},
		{
			name:     "Map_NestedStringIndex",
			path:     `self.foo["ba.r"]`,
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"ba.r": 123}, "another": "baz"},
		},
		{
			name:     "Map_NestedStringIndexNil",
			path:     `self.foo["bar"]`,
			obj:      map[string]any{"foo": nil, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": nil, "another": "baz"},
		},
		{
			name:     "Map_NestedStringIndexMissing",
			path:     `self.foo["bar"]`,
			obj:      map[string]any{"another": "baz"},
			value:    123,
			expected: map[string]any{"another": "baz"},
		},
		{
			name:         "Map_NestedStringIndexSingleQuotes",
			path:         `self.foo['bar']`,
			wantParseErr: true,
		},
		{
			name:     "Map_NestedStringIndexChain",
			path:     `self["foo"]["bar"]`,
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"bar": 123}, "another": "baz"},
		},
		{
			name:     "Slice_ScalarIndex",
			path:     "self.foo[1]",
			obj:      map[string]any{"foo": []any{1, 2, 3}},
			value:    123,
			expected: map[string]any{"foo": []any{1, 123, 3}},
		},
		{
			name:    "Slice_ScalarIndexOutOfRange",
			path:    "self.foo[9001]",
			obj:     map[string]any{"foo": []any{1, 2, 3}},
			value:   123,
			wantErr: true,
		},
		{
			name:     "Slice_NestedMap",
			path:     "self.foo[0].bar",
			obj:      map[string]any{"foo": []any{map[string]any{"bar": 1}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			value:    123,
			expected: map[string]any{"foo": []any{map[string]any{"bar": 123}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
		},
		{
			name:     "Slice_ScalarWildcard",
			path:     "self.foo[*]",
			obj:      map[string]any{"foo": []any{1, 2, 3}},
			value:    123,
			expected: map[string]any{"foo": []any{123, 123, 123}},
		},
		{
			name:     "Slice_MapWildcard",
			path:     "self.foo[*].bar",
			obj:      map[string]any{"foo": []any{map[string]any{"bar": 1}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			value:    123,
			expected: map[string]any{"foo": []any{map[string]any{"bar": 123}, map[string]any{"bar": 123}, map[string]any{"bar": 123}}},
		},
		{
			name:    "Slice_NonMapWildcard",
			path:    "self.foo[*]",
			obj:     map[string]any{"foo": 1},
			value:   123,
			wantErr: true,
		},
		{
			name: "Slice_WildcardAndOutOfRange",
			path: "self.foo[*][123]",
			obj: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": 234},
			}},
			wantErr: true,
		},
		{
			name: "Slice_MapMatcher",
			path: "self.foo[name=\"test-1\"].bar",
			obj: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": "test-1"},
				[]any{"string slice"},
				9001,
				true,
				map[string]any{"name": 234},
			}},
			value: 123,
			expected: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": "test-1", "bar": 123},
				[]any{"string slice"},
				9001,
				true,
				map[string]any{"name": 234},
			}},
		},

		{
			name: "Slice_MapMatcherEscapedQuote",
			path: "self.foo[name=\"test-\\\"-1\"].bar",
			obj: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": `test-"-1`},
				map[string]any{"name": 234},
			}},
			value: 123,
			expected: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": `test-"-1`, "bar": 123},
				map[string]any{"name": 234},
			}},
		},
		{
			name: "Slice_MapMatcherScalarAssignment",
			path: "self.foo[name=\"test-1\"]",
			obj: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": `test-1`},
				map[string]any{"name": 234},
			}},
			value: 123,
			expected: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				123,
				map[string]any{"name": 234},
			}},
		},
		{
			name: "Slice_MapMatcherMissing",
			path: "self.foo[name=\"test-1\"].bar",
			obj: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": 234},
			}},
			value: 123,
			expected: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": 234},
			}},
		},
		{
			name:    "Empty",
			path:    "",
			wantErr: true,
		},
		{
			name:     "Root",
			path:     "self",
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := ParsePathExpr(tc.path)
			if tc.wantParseErr {
				require.Error(t, err)
				return
			} else {
				require.NoError(t, err)
			}

			err = Apply(expr, tc.obj, tc.value)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expected, tc.obj)
			}
		})
	}
}

func TestOpApply(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name            string
		op              Op
		current         *unstructured.Unstructured
		mutated         *unstructured.Unstructured
		expectedMutated *unstructured.Unstructured
		wantErr         bool
	}{
		{
			name: "ConditionMet_MutationApplied",
			op: Op{
				Path:      mustParsePathExpr("self.foo"),
				Condition: mustParseCondition("true"),
				Value:     "bar",
			},
			current: &unstructured.Unstructured{Object: map[string]any{}},
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"foo": "bar",
			}},
		},
		{
			name: "ConditionNotMet_NoMutation",
			op: Op{
				Path:      mustParsePathExpr("self.foo"),
				Condition: mustParseCondition("false"),
				Value:     "bar",
			},
			current:         &unstructured.Unstructured{Object: map[string]any{}},
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "NilCurrentWithCondition_NoMutation",
			op: Op{
				Path:      mustParsePathExpr("self.foo"),
				Condition: mustParseCondition("true"),
				Value:     "bar",
			},
			current:         nil,
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "NoCondition_MutationApplied",
			op: Op{
				Path:  mustParsePathExpr("self.foo"),
				Value: "bar",
			},
			current: &unstructured.Unstructured{Object: map[string]any{}},
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"foo": "bar",
			}},
		},
		{
			name: "InvalidPath_Error",
			op: Op{
				Path:  mustParsePathExpr("invalid.foo"),
				Value: "bar",
			},
			current: &unstructured.Unstructured{Object: map[string]any{}},
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.op.Apply(ctx, tc.current, tc.mutated)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedMutated, tc.mutated)
			}
		})
	}
}

// TestInvalidPathInJson proves that the non-nil ops with nil paths left after failed unmarshalling will not panic when applied.
func TestInvalidPathInJson(t *testing.T) {
	overridesJson := "[\n  { \"path\": \"self.spec.template.spec.containers[name='operator'].resources.requests.cpu\", \"value\": \"250m\", \"condition\": \"self.spec.template.spec.containers[name='operator'].resources.requests.cpu == '250m'\" } ]"
	ops := []*Op{}
	err := json.Unmarshal([]byte(overridesJson), &ops)
	assert.Error(t, err)
	assert.Len(t, ops, 1)
	for _, op := range ops {
		assert.NoError(t, Apply(op.Path, map[string]any{}, op.Value))
	}
}

// helper functions for tests
func mustParsePathExpr(path string) *PathExpr {
	expr, err := ParsePathExpr(path)
	if err != nil {
		panic(fmt.Sprintf("failed to parse path expr %q: %v", path, err))
	}
	return expr
}

func mustParseCondition(cond string) cel.Program {
	prog, err := enocel.Parse(cond)
	if err != nil {
		panic(fmt.Sprintf("failed to parse condition %q: %v", cond, err))
	}
	return prog
}
