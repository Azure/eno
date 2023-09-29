package main

import (
	_ "embed"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Azure/eno/composition"
	apiv1 "github.com/Azure/eno/example/crd/api"
)

func main() {
	scheme := runtime.NewScheme()
	extv1.AddToScheme(scheme)
	apiv1.SchemeBuilder.AddToScheme(scheme)

	composition.MustGenerate(scheme, Generate)
}

//go:embed api/config/crd/example.azure.io_examples.yaml
var crdYaml []byte

func Generate(inputs *composition.Inputs) ([]client.Object, error) {
	cr := &apiv1.Example{}
	cr.Name = "example-resource"
	cr.Spec.Value = 123

	crd, err := composition.Parse(inputs, crdYaml)
	if err != nil {
		return nil, err
	}

	return []client.Object{crd, cr}, nil
}
