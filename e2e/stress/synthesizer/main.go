// Command eno-stress-synthesizer produces one deterministic ConfigMap from three inputs.
package main

import (
	"fmt"
	"os"

	"github.com/Azure/eno/pkg/function"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type inputs struct {
	Input1 *corev1.ConfigMap `eno_key:"input1"`
	Input2 *corev1.ConfigMap `eno_key:"input2"`
	Input3 *corev1.Secret    `eno_key:"input3"`
}

func synthesize(input inputs) ([]client.Object, error) {
	input1, found := input.Input1.Data["value"]
	if !found {
		return nil, fmt.Errorf("input1 ConfigMap has no data.value")
	}
	input2, found := input.Input2.Data["value"]
	if !found {
		return nil, fmt.Errorf("input2 ConfigMap has no data.value")
	}
	input3Raw, found := input.Input3.Data["value"]
	if !found {
		return nil, fmt.Errorf("input3 Secret has no data.value")
	}
	input3 := string(input3Raw)
	variation := os.Getenv("ENO_STRESS_VARIATION")
	outputName := os.Getenv("ENO_STRESS_OUTPUT_NAME")
	if outputName == "" {
		outputName = "combined-output"
	}

	output := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      outputName,
			Namespace: os.Getenv("COMPOSITION_NAMESPACE"),
			Labels: map[string]string{
				"stress.eno.azure.io/run-id":    os.Getenv("ENO_STRESS_RUN_ID"),
				"stress.eno.azure.io/variation": variation,
			},
		},
		Data: map[string]string{
			"input1":   input1,
			"input2":   input2,
			"input3":   input3,
			"combined": input1 + "-" + input2 + "-" + input3,
		},
	}
	return []client.Object{output}, nil
}

func main() {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	function.Main(synthesize, function.WithScheme(scheme))
}
