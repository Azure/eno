package cel

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var Env *cel.Env

func init() {
	initDefaultEnv()
}

func initDefaultEnv() {
	var err error
	Env, err = cel.NewEnv(cel.Variable("self", cel.DynType), cel.Variable("composition", cel.DynType))
	if err != nil {
		panic(fmt.Sprintf("failed to create default CEL environment: %v", err))
	}
}

func Parse(expr string) (cel.Program, error) {
	ast, iss := Env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	return Env.Program(ast, cel.InterruptCheckFrequency(10))
}

func Eval(ctx context.Context, prgm cel.Program, comp *apiv1.Composition, self *unstructured.Unstructured) (ref.Val, error) {
	val, _, err := prgm.Eval(map[string]any{
		"self":        self.Object,
		"composition": func() any { return newCompositionMap(comp) }, // cel will only execute this is the composition is referenced in the expression
	})
	return val, err
}

func newCompositionMap(comp *apiv1.Composition) map[string]any {
	m := map[string]any{
		"name":        comp.Name,
		"namespace":   comp.Namespace,
		"labels":      comp.Labels,
		"annotations": comp.Annotations,
	}

	if comp.ObjectMeta.DeletionTimestamp == nil {
		m["deletionTimestamp"] = nil
	} else {
		m["deletionTimestamp"] = comp.ObjectMeta.DeletionTimestamp.Time.Format(time.RFC3339)
	}

	return map[string]any{"metadata": m}
}
