package mutation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/go-logr/logr"
	"github.com/google/cel-go/cel"
	"google.golang.org/protobuf/types/known/structpb"
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
	StatusActive                 Status = "Active"
	StatusInactive               Status = "Inactive"
	StatusInvalidCondition       Status = "InvalidCondition"
	StatusMissingParent          Status = "MissingParent"
	StatusIndexOutOfRange        Status = "IndexOutOfRange"
	StatusPathTypeMismatch       Status = "PathTypeMismatch"
	StatusInvalidValueExpression Status = "InvalidValueExpression"
)

// Op is an operation that conditionally assigns a value to a path within an object.
// Designed to be sent over the wire as JSON.
type Op struct {
	Path            *PathExpr
	Condition       cel.Program
	Value           any
	ValueExpression cel.Program
}

type jsonOp struct {
	Path            string `json:"path"`
	Condition       string `json:"condition"`
	Value           any    `json:"value"`
	ValueExpression string `json:"valueExpression"`
}

func (o *Op) UnmarshalJSON(data []byte) error {
	var j jsonOp
	err := json.Unmarshal(data, &j)
	if err != nil {
		return err
	}

	if j.Value != nil && j.ValueExpression != "" {
		return fmt.Errorf("value and valueExpression are mutually exclusive for path %q", j.Path)
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

	if j.ValueExpression != "" {
		o.ValueExpression, err = enocel.Parse(j.ValueExpression)
		if err != nil {
			return fmt.Errorf("parsing valueExpression: %w", err)
		}
	}

	return nil
}

// Apply applies the operation to the "mutated" object if the condition is met by the "current" object.
func (o *Op) Apply(ctx context.Context, comp *apiv1.Composition, current, mutated *unstructured.Unstructured) (Status, error) {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("applying mutation operation", "path", o.Path.String(), "hasCondition", o.Condition != nil, "currentExists", current != nil)

	if o.Condition != nil {
		val, err := enocel.Eval(ctx, o.Condition, comp, current, o.Path)
		if err != nil {
			logger.Info("override condition evaluation failed", "path", o.Path.String(), "error", err, "currentExists", current != nil)
			return StatusInvalidCondition, nil
		}
		if b, ok := val.Value().(bool); !ok || !b {
			logger.Info("mutation condition not met, skipping", "path", o.Path.String(), "conditionResult", val.Value(), "resultType", fmt.Sprintf("%T", val.Value()))
			return StatusInactive, nil // condition not met
		}
	}
	logger.Info("applying mutation to path", "path", o.Path.String(), "valueType", fmt.Sprintf("%T", o.Value))
	resolvedValue := o.Value
	if o.ValueExpression != nil {
		if current == nil {
			logger.Info("skipping CEL value evaluation - current resource is nil", "path", o.Path.String())
			return StatusInactive, nil
		}
		val, err := enocel.Eval(ctx, o.ValueExpression, comp, current, o.Path)
		if err != nil {
			// Fail open: keep the synthesized/default value when user-provided CEL is invalid.
			logger.Error(err, "failed to evaluate value expression - skipping override and keeping synthesized value", "path", o.Path.String())
			return StatusInvalidValueExpression, nil
		}
		resolvedValue = val.Value()

		if resolvedValue == structpb.NullValue_NULL_VALUE {
			resolvedValue = nil
		}

		if resolvedValue == nil {
			// Treat null from valueExpression as "no override" (fail-open), not a delete.
			logger.Info("CEL value expression evaluated to null, skipping mutation", "path", o.Path.String())
			return StatusInactive, nil
		}

		logger.Info("override using valueExpression (resolved CEL value expression)", "path", o.Path.String())
	} else {
		logger.Info("override using static default value", "path", o.Path.String())
	}
	status, err := o.Path.Apply(mutated.Object, resolvedValue)

	if err != nil {
		// Fail open: invalid mutation paths should not block reconciliation.
		logger.Error(err, "failed to apply mutation - skipping override and keeping synthesized value", "path", o.Path.String(), "status", status)
		return status, nil
	}
	logger.Info("successfully applied mutation", "path", o.Path.String(), "status", status)
	return status, nil
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
