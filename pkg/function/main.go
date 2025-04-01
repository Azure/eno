package function

import (
	"fmt"
	"reflect"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Inputs is satisfied by any struct that defines the inputs required by a SynthFunc.
// Use the `eno_key` struct tag to specify the corresponding ref key for each input.
// Each field must either be a client.Object or a custom type registered with AddCustomInputType.
type Inputs interface{}

// SynthFunc defines a synthesizer function that takes a set of inputs and returns a list of objects.
type SynthFunc[T Inputs] func(inputs T) ([]client.Object, error)

// Main is the entrypoint for Eno synthesizer processes written using the framework defined by this package.
func Main[T Inputs](fn SynthFunc[T]) {
	ow := NewDefaultOutputWriter()
	ir, err := NewDefaultInputReader()
	if err != nil {
		panic(fmt.Sprintf("failed to create default input reader: %s", err))
	}

	err = main(fn, ir, ow)
	if err != nil {
		panic(fmt.Sprintf("error while calling synthesizer function: %s", err))
	}
}

func main[T Inputs](fn SynthFunc[T], ir *InputReader, ow *OutputWriter) error {
	var inputs T
	v := reflect.ValueOf(&inputs).Elem()
	t := v.Type()

	// Read the inputs
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tagValue := field.Tag.Get("eno_key")
		if tagValue == "" {
			continue
		}
		if v.Field(i).IsNil() {
			v.Field(i).Set(reflect.New(field.Type.Elem()))
		}
		input := v.Field(i).Interface()

		var obj client.Object
		var custom bool
		if o, ok := input.(client.Object); ok {
			obj = o
		} else if inputType, ok := customInputSourceTypes[v.Field(i).Type().String()]; ok {
			obj = reflect.New(inputType.Elem()).Interface().(client.Object)
			custom = true
		} else {
			return fmt.Errorf("input %s is not a known type", tagValue)
		}

		err := ReadInput[client.Object](ir, tagValue, obj)
		if err != nil {
			ow.AddResult(&krmv1.Result{
				Message:  fmt.Sprintf("error while reading input with key %q: %s", tagValue, err),
				Severity: krmv1.ResultSeverityError,
			})
			return ow.Write()
		}

		if custom {
			bind, ok := customInputBindings[v.Field(i).Type().String()]
			if !ok {
				return fmt.Errorf("no binding function for input %s", tagValue)
			}
			bound, err := bind(obj)
			if err != nil {
				return fmt.Errorf("error while binding custom input %s: %s", tagValue, err)
			}
			v.Field(i).Set(reflect.ValueOf(bound))
		}
	}

	// Call the fn and handle errors through the KRM interface
	outputs, err := fn(inputs)
	if err != nil {
		ow.AddResult(&krmv1.Result{
			Message:  err.Error(),
			Severity: krmv1.ResultSeverityError,
		})
		return ow.Write()
	}

	// Write the outputs
	for _, out := range outputs {
		ow.Add(out)
	}
	return ow.Write()
}

var customInputSourceTypes = map[string]reflect.Type{}
var customInputBindings = map[string]func(any) (any, error){}

// AddCustomInputType allows types that do not implement client.Object to be used as fields of Inputs structs.
func AddCustomInputType[Resource client.Object, Custom any](bind func(Resource) (Custom, error)) {
	str := reflect.TypeOf(bind).Out(0).String()

	// Map from custom type name to the underlying k8s input type
	var res Resource
	customInputSourceTypes[str] = reflect.TypeOf(res)

	// Map from the custom type name to the binding function
	customInputBindings[str] = func(in any) (any, error) {
		return bind(in.(Resource))
	}
}
