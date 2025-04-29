package main

import (
	"github.com/Azure/eno/pkg/function"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Inputs struct{}

func synthesize(inputs Inputs) ([]client.Object, error) {
	crds, err := function.ReadManifest("config/crd/example.azure.io_examples.yaml")
	if err != nil {
		return nil, err
	}

	cr := &Example{}
	cr.Name = "example-object"
	cr.Namespace = "default"
	cr.Spec.StringValue = "example-value"

	return append(crds, cr), nil
}

func main() {
	SchemeBuilder.AddToScheme(function.Scheme)
	function.Main(synthesize)
}
