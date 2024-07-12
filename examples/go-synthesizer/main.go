package main

import (
	"fmt"
	"strconv"

	apiv1 "github.com/Azure/eno/api/v1"
	"github.com/Azure/eno/pkg/function"
	corev1 "k8s.io/api/core/v1"
)

func main() {
	w := function.NewDefaultOutputWriter()
	r, err := function.NewDefaultInputReader()
	if err != nil {
		panic(err) // non-zero exits will be retried
	}

	input := &corev1.ConfigMap{}
	function.ReadInput(r, "example-input", input)

	replicas, _ := strconv.Atoi(input.Data["replicas"])

	for i := 0; i < replicas; i++ {
		comp := &apiv1.Composition{}
		comp.APIVersion = "eno.azure.io/v1"
		comp.Kind = "Composition"
		comp.Name = fmt.Sprintf("example-%d", i)
		comp.Namespace = "default"
		comp.Spec.Synthesizer.Name = "simple-example-synth"
		w.Add(comp)
	}

	w.Write()
}
