package function

import (
	"fmt"
	"os"
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
func Main[T Inputs](fn SynthFunc[T], opts ...mainOption) {
	// Process options
	options := &mainConfig{}
	for _, opt := range opts {
		opt(options)
	}

	ow := NewOutputWriter(os.Stdout, options.CompositeMungeFunc())
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
		tagValue := t.Field(i).Tag.Get("eno_key")
		if tagValue == "" {
			continue
		}

		input, err := newInput(ir, v.Field(i))
		if err != nil {
			return err
		}

		err = ReadInput(ir, tagValue, input.Object)
		if err != nil {
			ow.AddResult(&krmv1.Result{
				Message:  fmt.Sprintf("error while reading input with key %q: %s", tagValue, err),
				Severity: krmv1.ResultSeverityError,
			})
			return ow.Write()
		}

		input.Finalize()
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

type input struct {
	Object client.Object
	bindFn func(any) (any, error)
	field  reflect.Value
}

func newInput(ir *InputReader, field reflect.Value) (*input, error) {
	i := &input{field: field}

	// Allocate values for nil pointers
	if field.IsNil() {
		if field.Kind() == reflect.Slice {
			field.Set(reflect.MakeSlice(field.Type(), 0, 0))
		} else {
			field.Set(reflect.New(field.Type().Elem()))
		}
	}

	// Pass through client.Object types
	fieldVal := field.Interface()
	if o, ok := fieldVal.(client.Object); ok {
		i.Object = o
		return i, nil
	}

	// Resolve custom input types back to their binding functions
	name := field.Type().String()
	inputSourceType, ok := customInputSourceTypes[name]
	if !ok {
		return nil, fmt.Errorf("custom input type %q has not been registered", name)
	}

	fieldVal = reflect.New(inputSourceType.Elem()).Interface()
	i.Object = fieldVal.(client.Object)
	i.bindFn = customInputBindings[name]
	return i, nil
}

func (i *input) Finalize() error {
	if i.bindFn == nil {
		return nil
	}

	bound, err := i.bindFn(i.Object)
	if err != nil {
		return fmt.Errorf("error while binding custom input of type %T: %s", i.Object, err)
	}

	i.field.Set(reflect.ValueOf(bound))
	return nil
}
