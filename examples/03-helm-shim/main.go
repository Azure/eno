package main

import (
	"github.com/Azure/eno/pkg/function"
	"github.com/Azure/eno/pkg/helmshim"
	corev1 "k8s.io/api/core/v1"
)

type enoInputs struct {
	exmpleInput map[string]string `json:"exampleInput"`
}

func loadConfig(cm *corev1.ConfigMap) (map[string]string, error) {
	ei := map[string]string{}
	for k, v := range cm.Data {
		ei[k] = v
	}
	return ei, nil
}

func init() {
	function.AddCustomInputType(loadConfig)
}

func main() {
	// The Helm shim sets sane defaults, see helmshim.With* for overrides.

	synth := helmshim.Synth(func(ei enoInputs) (map[string]any, error) {
		return map[string]any{
			"myinput": ei.exmpleInput,
		}, nil
	})
	function.Main(synth)
}
