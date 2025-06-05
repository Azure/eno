package cel

import (
	"context"
	"fmt"

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
	Env, err = cel.NewEnv(cel.Variable("self", cel.DynType))
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

func Eval(ctx context.Context, prgm cel.Program, self *unstructured.Unstructured) (ref.Val, error) {
	val, _, err := prgm.Eval(map[string]any{"self": self.Object})
	return val, err
}
