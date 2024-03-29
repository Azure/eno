package readiness

import (
	"context"
	"sort"
	"time"

	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/google/cel-go/cel"
)

// Env encapsulates a CEL environment for use in readiness checks.
type Env struct {
	cel *cel.Env
}

func NewEnv() (*Env, error) {
	ce, err := cel.NewEnv(cel.Variable("self", cel.DynType))
	if err != nil {
		return nil, err
	}
	return &Env{cel: ce}, nil
}

// Check represents a parsed readiness check CEL expression.
type Check struct {
	Name    string
	program cel.Program
}

// ParseCheck parses the given CEL expression in the context of an environment,
// and returns a reusable execution handle.
func ParseCheck(env *Env, expr string) (*Check, error) {
	ast, iss := env.cel.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	prgm, err := env.cel.Program(ast, cel.InterruptCheckFrequency(10))
	if err != nil {
		return nil, err
	}
	return &Check{program: prgm}, nil
}

// Eval executes the compiled check against a given resource.
func (r *Check) Eval(ctx context.Context, resource *unstructured.Unstructured) (*Status, bool) {
	if resource == nil {
		return nil, false
	}
	val, _, err := r.program.ContextEval(ctx, map[string]any{"self": resource.Object})
	if err != nil {
		return nil, false
	}

	// Support matching on condition structs.
	// This allows us to grab the transition time instead of just using the current time.
	if list, ok := val.Value().([]ref.Val); ok {
		for _, ref := range list {
			if mp, ok := ref.Value().(map[string]any); ok {
				if mp != nil && mp["status"] == "True" && mp["type"] != nil && mp["reason"] != nil {
					ts := metav1.Now()
					if str, ok := mp["lastTransitionTime"].(string); ok {
						parsed, err := time.Parse(time.RFC3339, str)
						if err == nil {
							ts.Time = parsed
						}
					}
					return &Status{ReadyTime: ts, PreciseTime: err == nil}, true
				}
			}
		}
	}

	if val == celtypes.True {
		return &Status{ReadyTime: metav1.Now()}, true
	}
	return nil, false
}

type Checks []*Check

// Eval evaluates and prioritizes the set of readiness checks.
//
// - Nil is returned when less than all of the checks are ready
// - If some precise and some inprecise times are given, the precise times are favored
// - Within precise or non-precise times, the max of that group is always used
func (r Checks) Eval(ctx context.Context, resource *unstructured.Unstructured) (*Status, bool) {
	var all []*Status
	for _, check := range r {
		if ready, ok := check.Eval(ctx, resource); ok {
			all = append(all, ready)
		}
	}
	if len(all) == 0 || len(all) != len(r) {
		return nil, false
	}

	sort.Slice(all, func(i, j int) bool { return all[j].ReadyTime.Before(&all[i].ReadyTime) })

	// Use the max precise time if any are precise
	for _, ready := range all {
		ready := ready
		if !ready.PreciseTime {
			continue
		}
		return ready, true
	}

	// We don't have any precise times, fall back to the max
	return all[0], true
}

// EvalOptionally is identical to Eval, except it returns the current time in the status if no checks are set.
func (r Checks) EvalOptionally(ctx context.Context, resource *unstructured.Unstructured) (*Status, bool) {
	if len(r) == 0 {
		return &Status{ReadyTime: metav1.Now()}, true
	}
	return r.Eval(ctx, resource)
}

type Status struct {
	ReadyTime   metav1.Time
	PreciseTime bool // true when time came from a condition, not the controller's metav1.Now
}
