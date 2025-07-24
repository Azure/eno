package mutation

import (
	"bytes"
	"context"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v4/value"
)

// PathExpr represents an expression that can be used to access or modify values in a nested structure.
type PathExpr struct {
	ast *pathExprAST
}

// ParsePathExpr parses a path expression string.
//
// Supported syntax:
// - `field.anotherfield`: object field traversal
// - `field["anotherField"]` or `field['anotherField']`: alternative object field traversal (supports any field name including hyphens)
// - `field[2]`: array indexing
// - `field[*]`: array wildcards
// - `field[someKey="value"]`: object array field matchers
//
// Expressions can be chained, e.g. `field.anotherfield[2].yetAnotherField`.
// For field names containing special characters like hyphens, use bracket notation: `field['foo-bar']`.
func ParsePathExpr(expr string) (*PathExpr, error) {
	ast, err := parser.ParseString("", expr)
	if err != nil {
		return nil, err
	}
	return &PathExpr{ast: ast}, nil
}

var pathExprLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Ident", Pattern: `[a-zA-Z_][a-zA-Z0-9_]*`},
	{Name: "String", Pattern: `"([^"\\]|\\.)*"|'([^'\\]|\\.)*'`},
	{Name: "Int", Pattern: `\d+`},
	{Name: "Punct", Pattern: `[.\[\]=*]`},
	{Name: "whitespace", Pattern: `\s+`},
})

var parser = participle.MustBuild[pathExprAST](participle.Lexer(pathExprLexer))

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

func (p *PathExpr) ManagedByEno(ctx context.Context, current *unstructured.Unstructured) bool {
	if p == nil || current == nil {
		return false
	}

	smdPath, err := p.toSMDPath()
	if err != nil {
		logr.FromContextOrDiscard(ctx).V(0).Info("error while converting path expression to structured-merge-diff representation", "error", err)
		return true
	}

	managedFields := current.GetManagedFields()
	for _, field := range managedFields {
		if field.Manager == "eno" && field.FieldsV1 != nil {
			fieldSet := &fieldpath.Set{}
			if err := fieldSet.FromJSON(bytes.NewReader(field.FieldsV1.Raw)); err != nil {
				continue
			}
			if fieldSet.Has(smdPath) {
				return true
			}
		}
	}

	return false
}

func (p *PathExpr) toSMDPath() (fieldpath.Path, error) {
	sections := p.ast.Sections
	if len(sections) > 0 && sections[0].Field != nil && *sections[0].Field == "self" {
		sections = sections[1:]
	}

	chunks := []any{}
	for _, section := range sections {
		chunks = append(chunks, section.toPathElement())
	}
	return fieldpath.MakePath(chunks...)
}

func (s *section) toPathElement() fieldpath.PathElement {
	switch {
	case s.Field != nil:
		// Field names in dot notation are always unquoted identifiers
		return fieldpath.PathElement{FieldName: s.Field}

	case s.Index != nil:
		switch {
		case s.Index.Wildcard:
			return fieldpath.MatchAnyPathElement().PathElement
		case s.Index.Element != nil:
			return fieldpath.PathElement{Index: s.Index.Element}
		case s.Index.Key != nil:
			unquoted := unquoteKey(*s.Index.Key)
			return fieldpath.PathElement{FieldName: &unquoted}
		case s.Index.Matcher != nil:
			unquotedValue := unquoteKey(s.Index.Matcher.Value)
			fieldList := value.FieldList{{
				Name:  s.Index.Matcher.Key,
				Value: value.NewValueInterface(unquotedValue),
			}}
			return fieldpath.PathElement{Key: &fieldList}
		}
	}
	return fieldpath.PathElement{}
}
