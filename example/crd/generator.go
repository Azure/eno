package main

import (
	_ "embed"

	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/Azure/eno/example/crd/api"
	"github.com/Azure/eno/generation"
)

//go:embed api/config/crd/example.azure.io_examples.yaml
var crdYaml []byte

func main() {
	scheme := runtime.NewScheme()
	extv1.AddToScheme(scheme)
	apiv1.SchemeBuilder.AddToScheme(scheme)

	generation.MustGenerate(scheme, generation.WithStaticManifest(crdYaml, Generate))
}

func Generate(inputs *generation.Inputs) ([]client.Object, error) {
	cr := &apiv1.Example{}
	cr.Name = "example-resource"
	cr.Spec.Value = 123

	return []client.Object{cr}, nil
}
