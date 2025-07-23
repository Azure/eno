package mutation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enocel "github.com/Azure/eno/internal/cel"
)

var quotedStringRegex = regexp.MustCompile(`^(['"])(.*?)(['"])$`)

// Op is an operation that conditionally assigns a value to a path within an object.
// Designed to be sent over the wire as JSON.
type Op struct {
	Path      *PathExpr
	Condition cel.Program
	Value     any
}

type jsonOp struct {
	Path      string `json:"path"`
	Condition string `json:"condition"`
	Value     any    `json:"value"`
}

func (o *Op) UnmarshalJSON(data []byte) error {
	var j jsonOp
	err := json.Unmarshal(data, &j)
	if err != nil {
		return err
	}
	o.Value = j.Value

	o.Path, err = ParsePathExpr(j.Path)
	if err != nil {
		return fmt.Errorf("parsing path: %w", err)
	}

	if j.Condition != "" {
		o.Condition, err = enocel.Parse(j.Condition)
		if err != nil {
			return fmt.Errorf("parsing condition: %w", err)
		}
	}

	return nil
}

// Apply applies the operation to the "mutated" object if the condition is met by the "current" object.
func (o *Op) Apply(ctx context.Context, comp *apiv1.Composition, current, mutated *unstructured.Unstructured) error {
	if current == nil && o.Condition != nil {
		return nil // impossible condition
	}

	if o.Condition != nil {
		val, err := enocel.Eval(ctx, o.Condition, comp, current, o.Path)
		if err != nil {
			return nil // fail closed (too noisy to log)
		}
		if b, ok := val.Value().(bool); !ok || !b {
			return nil // condition not met
		}
	}

	return Apply(o.Path, mutated.Object, o.Value)
}

// unquoteKey removes quotes from a key string, handling both single and double quotes
func unquoteKey(key string) string {
	if matches := quotedStringRegex.FindStringSubmatch(key); matches != nil {
		// Ensure opening and closing quotes match
		if matches[1] == matches[3] {
			// For double quotes, use strconv.Unquote to handle escape sequences properly
			if matches[1] == `"` {
				if unquoted, err := strconv.Unquote(key); err == nil {
					return unquoted
				}
			}
			// For single quotes or if strconv.Unquote fails, return the content between quotes
			return matches[2]
		}
	}
	return key
}

// Apply applies a mutation i.e. sets the value(s) referred to by the path expression.
// Missing or nil values in the path will not be created, and will cause an error.
func Apply(path *PathExpr, obj, value any) error {
	if path == nil {
		return nil
	}

	if s := path.ast.Sections; len(s) == 0 || s[0].Field == nil || *s[0].Field != "self" {
		return fmt.Errorf("cannot apply mutation to non-self path")
	}

	copy := &PathExpr{ast: &pathExprAST{}}
	copy.ast.Sections = path.ast.Sections[1:] // remove the "self" section

	return apply(copy, 0, obj, value)
}

func apply(path *PathExpr, startIndex int, obj any, value any) error {
	state := obj

	for i, section := range path.ast.Sections[startIndex:] {
		// Map field indexing
		if section.Field != nil {
			m, ok := state.(map[string]any)
			if !ok {
				continue
			}
			if startIndex+i == len(path.ast.Sections)-1 {
				if value == nil {
					delete(m, *section.Field)
				} else {
					m[*section.Field] = value
				}
				return nil
			}
			state = m[*section.Field]
			continue
		}

		if section.Index == nil {
			continue // should be impossible
		}

		// Alternative map field indexing
		if key := section.Index.Key; key != nil {
			m, ok := state.(map[string]any)
			if !ok {
				continue
			}
			keyStr := unquoteKey(*key)
			if startIndex+i == len(path.ast.Sections)-1 {
				if value == nil {
					delete(m, keyStr)
				} else {
					m[keyStr] = value
				}
				return nil
			}
			state = m[keyStr]
			continue
		}

		slice, ok := state.([]any)
		if !ok {
			return fmt.Errorf("cannot apply wildcard to non-slice value")
		}

		// Simple array indexing
		if el := section.Index.Element; el != nil {
			if *el < 0 || *el >= len(slice) {
				return fmt.Errorf("index %d out of range for slice of length %d", *el, len(slice))
			}
			if startIndex+i == len(path.ast.Sections)-1 {
				slice[*el] = value
				return nil
			}
			state = slice[*el]
			continue
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
				expected, _ := strconv.Unquote(section.Index.Matcher.Value)
				if !ok || str != expected {
					continue // not matched by the matcher
				}
			}

			if isMap && startIndex+i < len(path.ast.Sections)-1 {
				err := apply(path, i+1, cur, value) // recurse into object
				if err != nil {
					return err
				}
				continue
			}
			slice[j] = value
		}
		break
	}

	return nil
}
