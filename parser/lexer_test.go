package parser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TODO: Test position

func TestLexer(t *testing.T) {
	tests := []struct {
		Name   string
		Lines  []string
		Tokens []token
		Error  string
	}{
		{
			Name:  "basic-ident",
			Lines: []string{"foo"},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{EOF: true},
			},
		},
		{
			Name:  "basic-mapping",
			Lines: []string{"foo: bar"},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "bar"},
				{EOF: true},
			},
		},
		{
			Name: "nested-mapping",
			Lines: []string{
				"\tfoo:",
				"    bar: baz",
				"  another: value",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},

				{Type: incrementIndentationToken, Value: ""},
				{Type: identToken, Value: "bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "baz"},

				{Type: decrementIndentationToken, Value: ""},
				{Type: identToken, Value: "another"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "value"},
				{EOF: true},
			},
		},
		{
			Name: "basic-list",
			Lines: []string{
				"- foo",
			},
			Tokens: []token{
				{Type: listSeparatorToken, Value: "-"},
				{Type: identToken, Value: "foo"},
				{EOF: true},
			},
		},
		{
			Name: "nested-list",
			Lines: []string{
				"- ",
				"  - foo",
			},
			Tokens: []token{
				{Type: listSeparatorToken, Value: "-"},
				{Type: incrementIndentationToken, Value: ""},
				{Type: listSeparatorToken, Value: "-"},
				{Type: identToken, Value: "foo"},
				{EOF: true},
			},
		},
		{
			Name: "dash-in-ident",
			Lines: []string{
				"- foo-",
			},
			Tokens: []token{
				{Type: listSeparatorToken, Value: "-"},
				{Type: identToken, Value: "foo-"},
				{EOF: true},
			},
		},
		{
			Name: "dash-in-ident-in-list",
			Lines: []string{
				"foo-",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo-"},
				{EOF: true},
			},
		},
		{
			Name: "colon-in-ident",
			Lines: []string{
				"foo:bar",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "bar"},
				{EOF: true},
			},
		},
		{
			Name: "colon-in-ident-inmapping",
			Lines: []string{
				"foo: bar:baz",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "bar:baz"},
				{EOF: true},
			},
		},
		{
			Name: "colon-in-string",
			Lines: []string{
				`"foo:bar"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: "foo:bar"},
				{EOF: true},
			},
		},
		{
			Name: "space-around-mapping-separator",
			Lines: []string{
				"foo  : \t bar",
			},
			Tokens: []token{
				// This is invalid syntax but should still lex without errors
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "bar"},
				{EOF: true},
			},
		},
		{
			Name: "quoted-mapping-keys",
			Lines: []string{
				`"foo bar": baz`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: "foo bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "baz"},
				{EOF: true},
			},
		},
		{
			Name: "double-quoted-string",
			Lines: []string{
				`"foo bar"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: "foo bar"},
				{EOF: true},
			},
		},
		{
			Name: "double-quoted-string-escaped-double-quote",
			Lines: []string{
				`"foo \" bar"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: `foo " bar`},
				{EOF: true},
			},
		},
		{
			Name: "space-in-idents",
			Lines: []string{
				`foo bar: baz another`,
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: identToken, Value: "bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "baz"},
				{Type: identToken, Value: "another"},
				{EOF: true},
			},
		},
		{
			Name: "extra-map-indentation",
			Lines: []string{
				"foo:",
				"    bar: baz", // 4 spaces
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: incrementIndentationToken, Value: ""},
				{Type: identToken, Value: "bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "baz"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-string-in-map",
			Lines: []string{
				"foo: |",
				"  line 1",
				"     line 2",
				"bar: baz",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: multilineStringFragmentToken, Value: "line 1\n   line 2\n"},

				{Type: identToken, Value: "bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "baz"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-string-in-list",
			Lines: []string{
				"- |",
				"  line 1",
				"     line 2",
				"- foo",
			},
			Tokens: []token{
				{Type: listSeparatorToken, Value: "-"},
				{Type: multilineStringFragmentToken, Value: "line 1\n   line 2\n"},

				{Type: listSeparatorToken, Value: "-"},
				{Type: identToken, Value: "foo"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-with-newline-char",
			Lines: []string{
				"foo:  |",
				"  bar\n",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: multilineStringFragmentToken, Value: "bar\n"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-string-trailing-chars",
			Lines: []string{
				"foo:  |  bar",
				"  baz",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: multilineStringFragmentToken, Value: "  bar\nbaz"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-string-trailing-whitespace",
			Lines: []string{
				"foo:  | ",
				"  baz",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: multilineStringFragmentToken, Value: " \nbaz"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-character-in-string",
			Lines: []string{
				`foo: "b|ar"`,
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: quotedStringFragmentToken, Value: "b|ar"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-character-in-ident",
			Lines: []string{
				`f|oo: `,
			},
			Tokens: []token{
				{Type: identToken, Value: "f|oo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{EOF: true},
			},
		},
		{
			Name: "multi-line-string-eof",
			Lines: []string{
				"foo:  |",
				"  bar",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: multilineStringFragmentToken, Value: "bar"},
				{EOF: true},
			},
		},
		{
			Name: "invalid-quoted-string-escape",
			Lines: []string{
				`"\f"`,
			},
			Error: ErrInvalidEscape.Error(),
		},
		{
			Name: "odd-indentation",
			Lines: []string{
				"   foo",
			},
			Error: ErrOddIndentation.Error(),
		},
		{
			Name: "odd-indentation-in-mapping",
			Lines: []string{
				"foo:",
				"   bar",
			},
			Error: ErrOddIndentation.Error(),
		},
		{
			Name: "simple-expression",
			Lines: []string{
				"{{foo}}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "simple-expression-with-spaces",
			Lines: []string{
				"{{ foo }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "multi-arg-expression",
			Lines: []string{
				"{{ foo bar }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "simple-expression-in-string",
			Lines: []string{
				`"foo{{bar}}baz"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: "foo"},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{Type: quotedStringFragmentToken, Value: "baz"},
				{EOF: true},
			},
		},
		{
			Name: "simple-expression-in-string-with-spaces",
			Lines: []string{
				`"foo {{ bar }} baz"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: "foo "},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{Type: quotedStringFragmentToken, Value: " baz"},
				{EOF: true},
			},
		},
		{
			Name: "expression-with-quoted-string",
			Lines: []string{
				`{{ foo "bar" }}`,
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: quotedStringFragmentToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "expression-in-string-with-string",
			Lines: []string{
				`"{{ foo "bar" }}"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: ""},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: quotedStringFragmentToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{Type: quotedStringFragmentToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "mapping-in-expression",
			Lines: []string{
				"{{ foo: bar }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo:"},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "empty",
			Lines: []string{
				"",
			},
			Tokens: []token{
				{EOF: true},
			},
		},
		{
			Name: "empty-string",
			Lines: []string{
				`""`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "newline-in-expression",
			Lines: []string{
				"{{ foo \n bar }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "expression-between-lines",
			Lines: []string{
				"- foo ",
				"- {{ bar }}",
				"- baz",
			},
			Tokens: []token{
				{Type: listSeparatorToken, Value: "-"},
				{Type: identToken, Value: "foo"},
				{Type: listSeparatorToken, Value: "-"},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{Type: listSeparatorToken, Value: "-"},
				{Type: identToken, Value: "baz"},
				{EOF: true},
			},
		},
		{
			Name: "expression-in-mapping",
			Lines: []string{
				"foo:",
				"  bar: {{ baz }}",
				"  {{ arg }}",
				"  another: mapping",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: incrementIndentationToken, Value: ""},

				{Type: identToken, Value: "bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "baz"},
				{Type: closeExpressionToken, Value: ""},

				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "arg"},
				{Type: closeExpressionToken, Value: ""},

				{Type: identToken, Value: "another"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "mapping"},
				{EOF: true},
			},
		},
		{
			Name: "expression-unindent",
			Lines: []string{
				"foo:",
				"  bar: {{ baz }}",
				"{{ arg }}",
				"  another: mapping",
			},
			Tokens: []token{
				{Type: identToken, Value: "foo"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: incrementIndentationToken, Value: ""},

				{Type: identToken, Value: "bar"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "baz"},
				{Type: closeExpressionToken, Value: ""},

				{Type: decrementIndentationToken, Value: ""},
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "arg"},
				{Type: closeExpressionToken, Value: ""},
				{Type: incrementIndentationToken, Value: ""},

				{Type: identToken, Value: "another"},
				{Type: mappingSeparatorToken, Value: ":"},
				{Type: identToken, Value: "mapping"},
				{EOF: true},
			},
		},
		{
			Name: "pipe-in-expr",
			Lines: []string{
				"{{ foo | bar }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: pipeToken, Value: "|"},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "pipe-in-expr-string",
			Lines: []string{
				`{{ "foo|bar" }}`,
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: quotedStringFragmentToken, Value: "foo|bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "mapping-in-expr", // it isn't supported
			Lines: []string{
				"{{ foo: bar }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo:"},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "list-in-expr", // this would be invalid, but should still lex without error
			Lines: []string{
				"{{ - foo }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "-"},
				{Type: identToken, Value: "foo"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "expr-assignment",
			Lines: []string{
				"{{ foo := bar }}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "foo"},
				{Type: assignmentOperatorToken, Value: ":="},
				{Type: identToken, Value: "bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "expr-assignment-in-str",
			Lines: []string{
				`{{ "foo := bar" }}`,
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: quotedStringFragmentToken, Value: "foo := bar"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "expr-newline-chomping",
			Lines: []string{
				"{{- foo -}}",
			},
			Tokens: []token{
				{Type: openExpressionToken, Value: ""},
				{Type: identToken, Value: "-"}, // Parser will implement semantics
				{Type: identToken, Value: "foo"},
				{Type: identToken, Value: "-"},
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "nested-expr",
			Lines: []string{
				"{{ {{ foo }} }}",
			},
			Error: ErrNestedExpression.Error(),
		},
		{
			Name: "early-expr-close",
			Lines: []string{
				"}}",
			},
			Tokens: []token{
				{Type: closeExpressionToken, Value: ""},
				{EOF: true},
			},
		},
		{
			Name: "comments",
			Lines: []string{
				"# foo",
				"bar #baz",
			},
			Tokens: []token{
				{Type: commentToken, Value: "foo"},
				{Type: identToken, Value: "bar"},
				{Type: commentToken, Value: "baz"},
				{EOF: true},
			},
		},
		{
			Name: "comments-in-string",
			Lines: []string{
				`"bar #baz"`,
			},
			Tokens: []token{
				{Type: quotedStringFragmentToken, Value: "bar #baz"},
				{EOF: true},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.Name, func(t *testing.T) {
			buf := []byte(strings.Join(tc.Lines, "\n"))
			lex := newLexer(buf)

			if tc.Error != "" {
				for {
					tok, err := lex.NextToken()
					if tok.EOF {
						t.Fatal("reached EOF before receiving any errors")
						return
					}
					if err == nil {
						continue
					}
					assert.EqualError(t, err, tc.Error)
					return
				}
			}

			actuals := make([]token, len(tc.Tokens))
			for i, expected := range tc.Tokens {
				actual, err := lex.NextToken()
				assert.NoError(t, err)

				// Asserting on position is optional
				if expected.Pos.Line == 0 {
					actual.Pos = expected.Pos
				} else {
					expected.Pos.Offset = actual.Pos.Offset // offset is not relevant to callers
				}
				actuals[i] = *actual
			}
			assert.Equal(t, tc.Tokens, actuals)
		})
	}
}
