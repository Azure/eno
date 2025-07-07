package mutation

import (
	"github.com/alecthomas/participle/v2"
)

// PathExpr represents an expression that can be used to access or modify values in a nested structure.
type PathExpr struct {
	ast *pathExprAST
}

// ParsePathExpr parses a path expression string.
//
// Supported syntax:
// - `field.anotherfield`: object field traversal
// - `field["anotherField"]`: alternative object field traversal (useful for values containing dots)
// - `field[2]`: array indexing
// - `field[*]`: array wildcards
// - `field[someKey="value"]`: object array field matchers
//
// Expressions can be chained, e.g. `field.anotherfield[2].yetAnotherField`.
func ParsePathExpr(expr string) (*PathExpr, error) {
	ast, err := parser.ParseString("", expr)
	if err != nil {
		return nil, err
	}
	return &PathExpr{ast: ast}, nil
}

var parser = participle.MustBuild[pathExprAST]()

type pathExprAST struct {
	Sections []*section `@@*`
}

type section struct {
	Field *string `"."* (@Ident`
	Index *index  `| "[" @@ "]")`
}

type index struct {
	Wildcard bool          `@"*"`
	Element  *int          `| @Int`
	Key      *string       `| @String`
	Matcher  *indexMatcher `| @@`
}

type indexMatcher struct {
	Key   string `@Ident "="`
	Value string `@String`
}
