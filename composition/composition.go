package composition

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type GenerateFn func(*Inputs) ([]client.Object, error)

func MustGenerate(scheme *runtime.Scheme, fn GenerateFn) {
	if err := Generate(scheme, fn); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

func Generate(scheme *runtime.Scheme, fn GenerateFn) error {
	codec := serializer.NewCodecFactory(scheme)
	dec := codec.UniversalDeserializer()

	inputs := []client.Object{}
	r := bufio.NewScanner(os.Stdin)
	for r.Scan() {
		obj, _, err := dec.Decode(r.Bytes(), &schema.GroupVersionKind{}, nil)
		if err != nil {
			return fmt.Errorf("decoding inputs: %w", err)
		}
		inputs = append(inputs, obj.(client.Object))
	}

	outputs, err := fn(&Inputs{
		Objects: inputs,
		scheme:  scheme,
	})
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	for _, out := range outputs {
		gvk, err := apiutil.GVKForObject(out, scheme)
		if err != nil {
			return err
		}
		out.GetObjectKind().SetGroupVersionKind(gvk)

		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("encoding inputs: %w", err)
		}
	}
	return nil
}

// TODO: Using Inputs to pass around the scheme is... not beautiful

type Inputs struct {
	Objects []client.Object
	scheme  *runtime.Scheme
}

func FindResource[T client.Object](inputs *Inputs, name string) T {
	for _, cur := range inputs.Objects {
		if cur.GetName() != name {
			continue
		}
		if ret, ok := cur.(T); ok {
			return ret
		}
	}

	var zero T
	panic(fmt.Errorf("expected an input resource %s of type %T but received none", name, zero))
}

func Parse(inputs *Inputs, yml []byte) (client.Object, error) {
	codec := serializer.NewCodecFactory(inputs.scheme)
	dec := codec.UniversalDeserializer()
	obj, _, err := dec.Decode(yml, &schema.GroupVersionKind{}, nil)
	if err != nil {
		return nil, err
	}

	return obj.(client.Object), nil
}
