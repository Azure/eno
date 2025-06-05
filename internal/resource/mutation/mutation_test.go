package mutation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApply(t *testing.T) {
	testCases := []struct {
		name     string
		path     string
		obj      map[string]any
		value    any
		expected map[string]any
		wantErr  bool
	}{
		{
			name:     "Map_TopLevel",
			path:     "/foo",
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{"foo": 123},
		},
		{
			name:     "Map_Nested",
			path:     "/foo/bar",
			obj:      map[string]any{"foo": map[string]any{}, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": map[string]any{"bar": 123}, "another": "baz"},
		},
		{
			name:     "Map_NestedNil",
			path:     "/foo/bar",
			obj:      map[string]any{"foo": nil, "another": "baz"},
			value:    123,
			expected: map[string]any{"foo": nil, "another": "baz"},
		},
		{
			name:     "Map_NestedMissing",
			path:     "/foo/bar",
			obj:      map[string]any{"another": "baz"},
			value:    123,
			expected: map[string]any{"another": "baz"},
		},
		{
			name:     "Slice_ScalarIndex",
			path:     "/foo[1]",
			obj:      map[string]any{"foo": []any{1, 2, 3}},
			value:    123,
			expected: map[string]any{"foo": []any{1, 123, 3}},
		},
		{
			name:    "Slice_ScalarIndexOutOfRange",
			path:    "/foo[9001]",
			obj:     map[string]any{"foo": []any{1, 2, 3}},
			value:   123,
			wantErr: true,
		},
		{
			name:     "Slice_NestedMap",
			path:     "/foo[0]/bar",
			obj:      map[string]any{"foo": []any{map[string]any{"bar": 1}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			value:    123,
			expected: map[string]any{"foo": []any{map[string]any{"bar": 123}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
		},
		{
			name:     "Slice_ScalarWildcard",
			path:     "/foo[*]",
			obj:      map[string]any{"foo": []any{1, 2, 3}},
			value:    123,
			expected: map[string]any{"foo": []any{123, 123, 123}},
		},
		{
			name:     "Slice_MapWildcard",
			path:     "/foo[*]/bar",
			obj:      map[string]any{"foo": []any{map[string]any{"bar": 1}, map[string]any{"bar": 2}, map[string]any{"bar": 3}}},
			value:    123,
			expected: map[string]any{"foo": []any{map[string]any{"bar": 123}, map[string]any{"bar": 123}, map[string]any{"bar": 123}}},
		},
		{
			name:    "Slice_NonMapWildcard",
			path:    "/foo[*]",
			obj:     map[string]any{"foo": 1},
			value:   123,
			wantErr: true,
		},
		{
			name: "Slice_MapMatcher",
			path: `/foo[name="test-1"]/bar`,
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
			path: `/foo[name="test-\"-1"]/bar`,
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
			path: `/foo[name="test-1"]`,
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
			path: `/foo[name="test-1"]/bar`,
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
			name:     "Empty",
			path:     "",
			obj:      map[string]any{},
			value:    123,
			expected: map[string]any{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := ParsePathExpr(tc.path)
			require.NoError(t, err)

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
