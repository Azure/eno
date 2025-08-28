package mutation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/google/cel-go/cel"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	enocel "github.com/Azure/eno/internal/cel"
)

var (
	quotedStringRegex       = regexp.MustCompile(`^(['"])(.*?)(['"])$`)
	escapedDoubleQuoteRegex = regexp.MustCompile(`\\"`)
	escapedSingleQuoteRegex = regexp.MustCompile(`\\'`)
)

type Status string

const (
	StatusActive           Status = "Active"
	StatusInactive         Status = "Inactive"
	StatusInvalidCondition Status = "InvalidCondition"
	StatusMissingParent    Status = "MissingParent"
	StatusIndexOutOfRange  Status = "IndexOutOfRange"
	StatusPathTypeMismatch Status = "PathTypeMismatch"
)

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
func (o *Op) Apply(ctx context.Context, comp *apiv1.Composition, current, mutated *unstructured.Unstructured) (Status, error) {
	logger := logr.FromContextOrDiscard(ctx)

	if o.Condition != nil {
		val, err := enocel.Eval(ctx, o.Condition, comp, current, o.Path)
		if err != nil && current == nil {
			if !strings.HasPrefix(err.Error(), "no such ") { // e.g. "no such property" or "no such key"
				logger.V(1).Info("override condition is invalid", "error", err)
			}
			return StatusInvalidCondition, nil
		}
		if b, ok := val.Value().(bool); !ok || !b {
			return StatusInactive, nil // condition not met
		}
	}

	return o.Path.Apply(mutated.Object, o.Value)
}

// unquoteKey removes quotes from a key string, handling both single and double quotes
func unquoteKey(key string) string {
	matches := quotedStringRegex.FindStringSubmatch(key)
	if matches == nil || matches[1] != matches[3] {
		return key
	}

	content := matches[2]
	switch matches[1] {
	case `"`:
		return escapedDoubleQuoteRegex.ReplaceAllString(content, `"`)
	case `'`:
		return escapedSingleQuoteRegex.ReplaceAllString(content, `'`)
	default:
		return content
	}
}
