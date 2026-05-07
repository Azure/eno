package mutation

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apiv1 "github.com/Azure/eno/api/v1"
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
		status                Status
	}{
		{
			name:     "Map_TopLevel",
			path:     "self.foo",
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{"foo": 123},
			status:   StatusActive,
		},
		{
			name:     "Map_Nil",
			path:     "self.foo",
			obj:      map[string]any{},
			value:    nil,
			expected: map[string]any{},
			status:   StatusActive,
		},
		{
			name:     "Alternative_Map_Nil",
			path:     `self["foo"]`,
			obj:      map[string]any{},
			value:    nil,
			expected: map[string]any{},
			status:   StatusActive,
		},
		{
			name:     "Map_Nested",
			path:     "self.foo.bar",
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"bar": 123}, "another": "baz"},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedNil",
			path:     "self.foo.bar",
			obj:      map[string]any{"foo": nil, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": nil, "another": "baz"},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedMissing",
			path:     "self.foo.bar",
			obj:      map[string]any{"another": "baz"},
			value:    123,
			expected: map[string]any{"another": "baz", "foo": map[string]any{"bar": 123}},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedStringIndex",
			path:     `self.foo["ba.r"]`,
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"ba.r": 123}, "another": "baz"},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedStringIndexNil",
			path:     `self.foo["bar"]`,
			obj:      map[string]any{"foo": nil, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": nil, "another": "baz"},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedStringIndexMissing",
			path:     `self.foo["bar"]`,
			obj:      map[string]any{"another": "baz"},
			value:    123,
			expected: map[string]any{"another": "baz", "foo": map[string]any{"bar": 123}},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedStringIndexSingleQuotes",
			path:     `self.foo['bar']`,
			obj:      map[string]any{"foo": map[string]any{"bar": "old"}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"bar": 123}, "another": "baz"},
			status:   StatusActive,
		},
		{
			name:     "Map_NestedStringIndexChain",
			path:     `self["foo"]["bar"]`,
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"bar": 123}, "another": "baz"},
			status:   StatusActive,
		},
		{
			name:     "Map_BracketNotationWithHyphens",
			path:     `self["foo-bar"]`,
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{"foo-bar": 123},
			status:   StatusActive,
		},
		{
			name:     "Map_SingleQuoteBracketNotationWithHyphens",
			path:     `self['foo-bar']`,
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{"foo-bar": 123},
			status:   StatusActive,
		},
		{
			name:     "Slice_ScalarIndex",
			path:     "self.foo[1]",
			obj:      map[string]any{"foo": []any{1, 2, 3}},
			value:    123,
			expected: map[string]any{"foo": []any{1, 123, 3}},
			status:   StatusActive,
		},
		{
			name:    "Slice_ScalarIndexOutOfRange",
			path:    "self.foo[9001]",
			obj:     map[string]any{"foo": []any{1, 2, 3}},
			value:   123,
			wantErr: true,
			status:  StatusIndexOutOfRange,
		},
		{
			name:     "Slice_NestedMap",
			path:     "self.foo[0].bar",
			obj:      map[string]any{"foo": []any{map[string]any{"bar": 1}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			value:    123,
			expected: map[string]any{"foo": []any{map[string]any{"bar": 123}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			status:   StatusActive,
		},
		{
			name:     "Slice_ScalarWildcard",
			path:     "self.foo[*]",
			obj:      map[string]any{"foo": []any{1, 2, 3}},
			value:    123,
			expected: map[string]any{"foo": []any{123, 123, 123}},
			status:   StatusActive,
		},
		{
			name:     "Slice_MapWildcard",
			path:     "self.foo[*].bar",
			obj:      map[string]any{"foo": []any{map[string]any{"bar": 1}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			value:    123,
			expected: map[string]any{"foo": []any{map[string]any{"bar": 123}, map[string]any{"bar": 123}, map[string]any{"bar": 123}}},
			status:   StatusActive,
		},
		{
			name:    "Slice_NonMapWildcard",
			path:    "self.foo[*]",
			obj:     map[string]any{"foo": 1},
			value:   123,
			wantErr: true,
			status:  StatusPathTypeMismatch,
		},
		{
			name: "Slice_WildcardAndOutOfRange",
			path: "self.foo[*][123]",
			obj: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": 234},
			}},
			wantErr: true,
			status:  StatusPathTypeMismatch,
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
			value:  123,
			status: StatusActive,
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
			value:  123,
			status: StatusActive,
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
			value:  123,
			status: StatusActive,
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
			value:  123,
			status: StatusActive,
			expected: map[string]any{"foo": []any{
				map[string]any{"name": "test-2"},
				map[string]any{"name": 234},
			}},
		},
		{
			name:   "Complex_Nil",
			path:   "self.spec.template.spec.containers[name='foo'].resources.limits.cpu",
			value:  nil,
			status: StatusActive,
			obj: map[string]any{
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]any{
							"foo": "bar",
						},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]any{
								"foo": "bar",
							},
						},
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name":  "foo",
									"image": "bar",
									"resources": map[string]any{
										"requests": map[string]any{
											"cpu": "5m",
										},
										"limits": map[string]any{
											"cpu": "10m",
										},
									},
								},
							},
						},
					},
				},
			},
			expected: map[string]any{
				"spec": map[string]any{
					"selector": map[string]any{
						"matchLabels": map[string]any{
							"foo": "bar",
						},
					},
					"template": map[string]any{
						"metadata": map[string]any{
							"labels": map[string]any{
								"foo": "bar",
							},
						},
						"spec": map[string]any{
							"containers": []any{
								map[string]any{
									"name":  "foo",
									"image": "bar",
									"resources": map[string]any{
										"requests": map[string]any{
											"cpu": "5m",
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name:     "Map_CreateMissingIntermediateMap",
			path:     "self.spec.minAllowed.memory",
			obj:      map[string]any{"spec": map[string]any{}},
			value:    "20Mi",
			expected: map[string]any{"spec": map[string]any{"minAllowed": map[string]any{"memory": "20Mi"}}},
			status:   StatusActive,
		},
		{
			name:     "Map_CreateMissingIntermediateMap_NilValue",
			path:     "self.spec.minAllowed.memory",
			obj:      map[string]any{"spec": map[string]any{}},
			value:    nil,
			expected: map[string]any{"spec": map[string]any{}},
			status:   StatusActive,
		},
		{
			name:     "Map_CreateDeeplyNestedMissingMaps",
			path:     "self.a.b.c.d",
			obj:      map[string]any{},
			value:    "val",
			expected: map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "val"}}}},
			status:   StatusActive,
		},
		{
			name:     "Root",
			path:     "self",
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{},
			status:   StatusMissingParent,
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

			status, err := expr.Apply(tc.obj, tc.value)
			assert.Equal(t, tc.status, status)

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
			name: "NilCurrentWithStaticCondition_MutationApplied",
			op: Op{
				Path:      mustParsePathExpr("self.foo"),
				Condition: mustParseCondition("true"),
				Value:     "bar",
			},
			current:         nil,
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{"foo": "bar"}},
		},
		{
			name: "NilCurrentWithCondition_NoMutation",
			op: Op{
				Path:      mustParsePathExpr("self.foo"),
				Condition: mustParseCondition("self.bar"),
				Value:     "bar",
			},
			current:         nil,
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "NilCurrentWithFalsyCondition_NoMutation",
			op: Op{
				Path:      mustParsePathExpr("self.foo"),
				Condition: mustParseCondition("false"),
				Value:     "bar",
			},
			current:         nil,
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "NilCurrentWithNoCondition_MutationApplied",
			op: Op{
				Path:  mustParsePathExpr("self.foo"),
				Value: "bar",
			},
			current: nil,
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"foo": "bar",
			}},
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
			name: "CELValueExpression_ResolvesFromCurrent",
			op: Op{
				Path:            mustParsePathExpr("self.foo"),
				ValueExpression: mustParseCEL("self.bar"),
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"bar": "resolved-value",
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"foo": "resolved-value",
			}},
		},
		{
			name: "CELValueExpression_NilCurrent_Skipped",
			op: Op{
				Path:            mustParsePathExpr("self.foo"),
				ValueExpression: mustParseCEL("self.bar"),
			},
			current:         nil,
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "CELValueExpression_WithCondition",
			op: Op{
				Path:            mustParsePathExpr("self.foo"),
				Condition:       mustParseCEL("has(self.bar)"),
				ValueExpression: mustParseCEL("self.bar"),
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"bar": "dynamic-val",
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"foo": "dynamic-val",
			}},
		},
		{
			name: "CELValueExpression_NullResult_SkipsMutation",
			op: Op{
				Path:            mustParsePathExpr("self.foo"),
				ValueExpression: mustParseCEL("null"),
			},
			current:         &unstructured.Unstructured{Object: map[string]any{"bar": "val"}},
			mutated:         &unstructured.Unstructured{Object: map[string]any{"foo": "old-value"}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{"foo": "old-value"}},
		},
		{
			name: "CELValueExpression_NullResult_NoFieldToDelete",
			op: Op{
				Path:            mustParsePathExpr("self.foo"),
				ValueExpression: mustParseCEL("null"),
			},
			current:         &unstructured.Unstructured{Object: map[string]any{"bar": "val"}},
			mutated:         &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "StaticNilValue_DeletesField",
			op: Op{
				Path:  mustParsePathExpr("self.foo"),
				Value: nil,
			},
			current:         &unstructured.Unstructured{Object: map[string]any{"bar": "val"}},
			mutated:         &unstructured.Unstructured{Object: map[string]any{"foo": "old-value"}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{}},
		},
		{
			name: "ConditionMet_AnnotationExists_MutationApplied",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"override-min-max": "enabled",
					},
				},
				"spec": map[string]any{},
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{"minAllowed": map[string]any{"cpu": "100m"}},
			}},
		},
		{
			name: "ConditionNotMet_AnnotationDisabled_NoMutation",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"override-min-max": "disabled",
					},
				},
				"spec": map[string]any{},
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
		},
		{
			name: "Bug1_AnnotationKeyMissing_CurrentExists_SilentlyInactive",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{},
				},
				"spec": map[string]any{},
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
		},
		{
			name: "Bug1_NoAnnotationsMap_CurrentExists_SilentlyInactive",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{},
				"spec":     map[string]any{},
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
		},
		{
			name: "Bug1_NilCurrent_ConditionRefsSelf_InvalidCondition",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: nil,
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
		},
		{
			name: "HasGuard_AnnotationMissing_ConditionFalse_NoMutation",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`has(self.metadata.annotations) && 'override-min-max' in self.metadata.annotations && self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{},
				"spec":     map[string]any{},
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
		},
		{
			name: "HasGuard_AnnotationPresent_ConditionTrue_MutationApplied",
			op: Op{
				Path:      mustParsePathExpr("self.spec.minAllowed.cpu"),
				Condition: mustParseCEL(`has(self.metadata.annotations) && 'override-min-max' in self.metadata.annotations && self.metadata.annotations['override-min-max'] == 'enabled'`),
				Value:     "100m",
			},
			current: &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"override-min-max": "enabled",
					},
				},
				"spec": map[string]any{},
			}},
			mutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{},
			}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"spec": map[string]any{"minAllowed": map[string]any{"cpu": "100m"}},
			}},
		},
		{
			name: "StaticValue_StillWorks",
			op: Op{
				Path:  mustParsePathExpr("self.foo"),
				Value: "static-val",
			},
			current: &unstructured.Unstructured{Object: map[string]any{}},
			mutated: &unstructured.Unstructured{Object: map[string]any{}},
			expectedMutated: &unstructured.Unstructured{Object: map[string]any{
				"foo": "static-val",
			}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.op.Apply(ctx, &apiv1.Composition{}, tc.current, tc.mutated)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedMutated, tc.mutated)
			}

			// Also make sure the path can be successfully converted to the SMD representation
			_, err = tc.op.Path.toSMDPath()
			assert.NoError(t, err)
		})
	}
}

func TestOpApplyValueExpressionErrorFailsOpen(t *testing.T) {
	ctx := context.Background()

	op := Op{
		Path:            mustParsePathExpr("self.foo"),
		ValueExpression: mustParseCEL("self.bar.baz"),
	}

	current := &unstructured.Unstructured{Object: map[string]any{
		"bar": "not-a-map",
	}}
	mutated := &unstructured.Unstructured{Object: map[string]any{
		"foo": "synthesized-default",
	}}

	status, err := op.Apply(ctx, &apiv1.Composition{}, current, mutated)
	require.NoError(t, err)
	assert.Equal(t, StatusInvalidValueExpression, status)
	assert.Equal(t, map[string]any{"foo": "synthesized-default"}, mutated.Object)
}

func TestOpApplyPathTypeMismatchFailsOpen(t *testing.T) {
	ctx := context.Background()

	op := Op{
		Path:  mustParsePathExpr("self.foo[*]"),
		Value: "new-value",
	}

	current := &unstructured.Unstructured{Object: map[string]any{}}
	mutated := &unstructured.Unstructured{Object: map[string]any{
		"foo": "scalar",
	}}

	status, err := op.Apply(ctx, &apiv1.Composition{}, current, mutated)
	require.NoError(t, err)
	assert.Equal(t, StatusPathTypeMismatch, status)
	assert.Equal(t, map[string]any{"foo": "scalar"}, mutated.Object)
}

// TestInvalidPathInJson proves that the non-nil ops with nil paths left after failed unmarshalling will not panic when applied.
func TestInvalidPathInJson(t *testing.T) {
	overridesJson := "[\n  { \"path\": \"self.spec.template.spec.containers[name='operator'].resources.requests.cpu\", \"value\": \"250m\", \"condition\": \"self.spec.template.spec.containers[name='operator'].resources.requests.cpu == '250m'\" } ]"
	ops := []*Op{}
	err := json.Unmarshal([]byte(overridesJson), &ops)
	assert.Error(t, err)
	assert.Len(t, ops, 1)
	for _, op := range ops {
		// The main goal is to ensure no panic occurs, errors are acceptable
		op.Path.Apply(map[string]any{}, op.Value)
	}
}

func TestVPAOverrideMatrix(t *testing.T) {
	ctx := context.Background()

	const (
		defaultMin  = "10Mi"
		defaultMax  = "30Mi"
		defaultMode = "Recreate"
	)

	ops := []Op{
		{
			Path:            mustParsePathExpr(`self.spec.resourcePolicy.containerPolicies[containerName="cost-analysis-agent"].minAllowed.memory`),
			Condition:       mustParseCEL(`self.metadata.annotations['autoscaler.addons.k8s.io/override-min-max'] == 'enabled'`),
			ValueExpression: mustParseCEL(`self.spec.resourcePolicy.containerPolicies[0].minAllowed.memory`),
		},
		{
			Path:            mustParsePathExpr(`self.spec.resourcePolicy.containerPolicies[containerName="cost-analysis-agent"].maxAllowed.memory`),
			Condition:       mustParseCEL(`self.metadata.annotations['autoscaler.addons.k8s.io/override-min-max'] == 'enabled'`),
			ValueExpression: mustParseCEL(`self.spec.resourcePolicy.containerPolicies[0].maxAllowed.memory`),
		},
		{
			Path:            mustParsePathExpr("self.spec.updatePolicy.updateMode"),
			Condition:       mustParseCEL(`self.metadata.annotations['autoscaler.addons.k8s.io/override-update-mode'] == 'enabled'`),
			ValueExpression: mustParseCEL("self.spec.updatePolicy.updateMode"),
		},
	}

	testCases := []struct {
		name               string
		minMaxOverride     string
		updateModeOverride string
		currentMin         string
		currentMax         string
		currentUpdateMode  string
		expectedMin        string
		expectedMax        string
		expectedUpdateMode string
	}{
		{name: "Case01_EnabledEnabled", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "99Mi", currentMax: "99Mi", currentUpdateMode: "Off", expectedMin: "99Mi", expectedMax: "99Mi", expectedUpdateMode: "Off"},
		{name: "Case02_DisabledDisabled", minMaxOverride: "disabled", updateModeOverride: "disabled", currentMin: "99Mi", currentMax: "99Mi", currentUpdateMode: "Off", expectedMin: defaultMin, expectedMax: defaultMax, expectedUpdateMode: defaultMode},
		{name: "Case03_EnabledDisabled", minMaxOverride: "enabled", updateModeOverride: "disabled", currentMin: "99Mi", currentMax: "99Mi", currentUpdateMode: "Off", expectedMin: "99Mi", expectedMax: "99Mi", expectedUpdateMode: defaultMode},
		{name: "Case04_DisabledEnabled", minMaxOverride: "disabled", updateModeOverride: "enabled", currentMin: "99Mi", currentMax: "99Mi", currentUpdateMode: "Off", expectedMin: defaultMin, expectedMax: defaultMax, expectedUpdateMode: "Off"},
		{name: "Case05_EnabledEnabled_ChangeMin", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "50Mi", currentMax: "30Mi", currentUpdateMode: "Recreate", expectedMin: "50Mi", expectedMax: "30Mi", expectedUpdateMode: "Recreate"},
		{name: "Case06_EnabledEnabled_ChangeMax", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "10Mi", currentMax: "100Mi", currentUpdateMode: "Recreate", expectedMin: "10Mi", expectedMax: "100Mi", expectedUpdateMode: "Recreate"},
		{name: "Case07_EnabledEnabled_ChangeMode", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "10Mi", currentMax: "30Mi", currentUpdateMode: "Auto", expectedMin: "10Mi", expectedMax: "30Mi", expectedUpdateMode: "Auto"},
		{name: "Case08_EnabledEnabled_ChangeMinMax", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "20Mi", currentMax: "60Mi", currentUpdateMode: "Recreate", expectedMin: "20Mi", expectedMax: "60Mi", expectedUpdateMode: "Recreate"},
		{name: "Case09_EnabledEnabled_ChangeMinMode", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "25Mi", currentMax: "30Mi", currentUpdateMode: "Initial", expectedMin: "25Mi", expectedMax: "30Mi", expectedUpdateMode: "Initial"},
		{name: "Case10_EnabledEnabled_ChangeAll", minMaxOverride: "enabled", updateModeOverride: "enabled", currentMin: "15Mi", currentMax: "50Mi", currentUpdateMode: "Auto", expectedMin: "15Mi", expectedMax: "50Mi", expectedUpdateMode: "Auto"},
		{name: "Case11_DisabledDisabled_ChangeAll", minMaxOverride: "disabled", updateModeOverride: "disabled", currentMin: "15Mi", currentMax: "50Mi", currentUpdateMode: "Auto", expectedMin: defaultMin, expectedMax: defaultMax, expectedUpdateMode: defaultMode},
		{name: "Case12_EnabledDisabled_ChangeAll", minMaxOverride: "enabled", updateModeOverride: "disabled", currentMin: "15Mi", currentMax: "50Mi", currentUpdateMode: "Auto", expectedMin: "15Mi", expectedMax: "50Mi", expectedUpdateMode: defaultMode},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			current := buildCostAnalysisVPA(tc.minMaxOverride, tc.updateModeOverride, tc.currentMin, tc.currentMax, tc.currentUpdateMode)
			mutated := buildCostAnalysisVPA(tc.minMaxOverride, tc.updateModeOverride, defaultMin, defaultMax, defaultMode)

			for i := range ops {
				_, err := ops[i].Apply(ctx, &apiv1.Composition{}, current, mutated)
				require.NoError(t, err)
			}

			gotMin, gotMax, gotMode := readCostAnalysisVPA(mutated)
			assert.Equal(t, tc.expectedMin, gotMin)
			assert.Equal(t, tc.expectedMax, gotMax)
			assert.Equal(t, tc.expectedUpdateMode, gotMode)
		})
	}
}

func buildCostAnalysisVPA(minMaxOverride, updateModeOverride, min, max, updateMode string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				"autoscaler.addons.k8s.io/override-min-max":     minMaxOverride,
				"autoscaler.addons.k8s.io/override-update-mode": updateModeOverride,
			},
		},
		"spec": map[string]any{
			"updatePolicy": map[string]any{
				"updateMode": updateMode,
			},
			"resourcePolicy": map[string]any{
				"containerPolicies": []any{
					map[string]any{
						"containerName": "cost-analysis-agent",
						"minAllowed":    map[string]any{"memory": min},
						"maxAllowed":    map[string]any{"memory": max},
					},
				},
			},
		},
	}}
}

func readCostAnalysisVPA(obj *unstructured.Unstructured) (min, max, mode string) {
	mode, _, _ = unstructured.NestedString(obj.Object, "spec", "updatePolicy", "updateMode")
	policies, _, _ := unstructured.NestedSlice(obj.Object, "spec", "resourcePolicy", "containerPolicies")
	for _, p := range policies {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if m["containerName"] != "cost-analysis-agent" {
			continue
		}
		if minAllowed, ok := m["minAllowed"].(map[string]any); ok {
			if v, ok := minAllowed["memory"].(string); ok {
				min = v
			}
		}
		if maxAllowed, ok := m["maxAllowed"].(map[string]any); ok {
			if v, ok := maxAllowed["memory"].(string); ok {
				max = v
			}
		}
		break
	}
	return min, max, mode
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

func mustParseCEL(expr string) cel.Program {
	prog, err := enocel.Parse(expr)
	if err != nil {
		panic(fmt.Sprintf("failed to parse CEL expression %q: %v", expr, err))
	}
	return prog
}
