package cel

import (
	"context"
	"fmt"
	"time"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var Env *cel.Env

type FieldMetadata interface {
	ManagedByEno(context.Context, *unstructured.Unstructured) bool
}

func init() {
	initDefaultEnv()
}

func initDefaultEnv() {
	var err error
	Env, err = cel.NewEnv(
		ext.Encoders(),
		ext.Lists(),
		ext.Strings(),
		cel.Variable("self", cel.DynType),
		cel.Variable("composition", cel.DynType),
		cel.Variable("pathManagedByEno", cel.BoolType),
		cel.Function("compareResourceQuantities",
			cel.Overload("compare_resource_quantities_equal_string_string",
				[]*cel.Type{cel.StringType, cel.StringType}, cel.IntType,
				cel.BinaryBinding(compareResources))),
	)
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

func Eval(ctx context.Context, prgm cel.Program, comp *apiv1.Composition, self *unstructured.Unstructured, fm FieldMetadata) (ref.Val, error) {
	args := map[string]any{
		"composition": func() any { return newCompositionMap(comp) }, // cel will only execute this if the composition is referenced in the expression
	}
	if self != nil {
		args["self"] = self.Object
	}
	if fm != nil {
		args["pathManagedByEno"] = func() any { return fm.ManagedByEno(ctx, self) }
	}
	val, _, err := prgm.Eval(args)
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

func compareResources(lhs ref.Val, rhs ref.Val) ref.Val {
	lStr, _ := lhs.Value().(string)
	rStr, _ := rhs.Value().(string)

	l, err := resource.ParseQuantity(lStr)
	if err != nil {
		return types.WrapErr(fmt.Errorf("parsing left quantity: %w", err))
	}

	r, err := resource.ParseQuantity(rStr)
	if err != nil {
		return types.WrapErr(fmt.Errorf("parsing right quantity: %w", err))
	}

	return types.Int(l.Cmp(r))
}
