package mutation

import (
	"bytes"
	"context"
	"fmt"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v4/fieldpath"
	"sigs.k8s.io/structured-merge-diff/v4/value"
)

// PathExpr represents an expression that can be used to access or modify values in a nested structure.
type PathExpr struct {
	ast  *pathExprAST
	expr string
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
	if s := ast.Sections; len(s) == 0 || s[0].Field == nil || *s[0].Field != "self" {
		return nil, fmt.Errorf("paths must start with `self.`")
	}
	return &PathExpr{ast: ast, expr: expr}, nil
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

func (p *PathExpr) String() string { return p.expr }

func (p *PathExpr) ManagedByEno(ctx context.Context, current *unstructured.Unstructured) bool {
	if p == nil || current == nil {
		return false
	}

	smdPath, err := p.toSMDPath()
	if err != nil {
		logr.FromContextOrDiscard(ctx).Error(err, "error while converting path expression to structured-merge-diff representation")
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

// Apply applies a mutation i.e. sets the value(s) referred to by the path expression.
// Missing or nil values in the path will not be created, and will cause an error.
func (p *PathExpr) Apply(obj, value any) (Status, error) {
	if p == nil {
		return StatusInactive, nil
	}

	copy := &PathExpr{ast: &pathExprAST{}}
	copy.ast.Sections = p.ast.Sections[1:] // remove the "self" section

	return p.apply(copy, 0, obj, value)
}

func (p *PathExpr) apply(path *PathExpr, startIndex int, obj any, value any) (Status, error) {
	for i, section := range path.ast.Sections[startIndex:] {
		isLastSection := startIndex+i == len(path.ast.Sections)-1

		// Map field indexing
		if section.Field != nil || (section.Index != nil && section.Index.Key != nil) {
			m, ok := obj.(map[string]any)
			if !ok {
				continue
			}

			var keyStr string
			if section.Field != nil {
				keyStr = *section.Field
			} else {
				keyStr = unquoteKey(*section.Index.Key)
			}

			if isLastSection {
				if value == nil {
					delete(m, keyStr)
				} else {
					m[keyStr] = value
				}
				return StatusActive, nil
			}

			child := m[keyStr]
			if child != nil {
				status, err := p.apply(path, startIndex+i+1, child, value)
				if err != nil {
					return status, err
				}
				if value == nil {
					if nextMap, ok := child.(map[string]any); ok && len(nextMap) == 0 {
						delete(m, keyStr)
					}
				}
			}
			return StatusActive, nil
		}

		if section.Index == nil {
			continue
		}

		slice, ok := obj.([]any)
		if !ok {
			return StatusPathTypeMismatch, fmt.Errorf("cannot apply wildcard to non-slice value")
		}

		// Simple array indexing
		if el := section.Index.Element; el != nil {
			if *el < 0 || *el >= len(slice) {
				return StatusIndexOutOfRange, fmt.Errorf("index %d out of range for slice of length %d", *el, len(slice))
			}
			if isLastSection {
				slice[*el] = value
				return StatusActive, nil
			}
			nextState := slice[*el]
			if nextState != nil {
				status, err := p.apply(path, startIndex+i+1, nextState, value)
				if err != nil {
					return status, err
				}
			}
			return StatusActive, nil
		}

		// Complex array indexing (wildcard or matcher)
		if !section.Index.Wildcard && section.Index.Matcher == nil {
			continue // should be impossible
		}
		for j, cur := range slice {
			m, isMap := cur.(map[string]any)

			if section.Index.Matcher != nil {
				if !isMap {
					continue // can't apply matcher to non-map value
				}
				val := m[section.Index.Matcher.Key]
				str, ok := val.(string)
				expected := unquoteKey(section.Index.Matcher.Value)
				if !ok || str != expected {
					continue // not matched by the matcher
				}
			}

			if isMap && !isLastSection {
				status, err := p.apply(path, startIndex+i+1, cur, value)
				if err != nil {
					return status, err
				}
				continue
			}
			slice[j] = value
		}
		break
	}

	return StatusMissingParent, nil
}
