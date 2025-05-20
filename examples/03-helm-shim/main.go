package main

import (
	"github.com/Azure/eno/pkg/function"
	"github.com/Azure/eno/pkg/helmshim"
)

type enoInputs struct{}

func main() {
	// The Helm shim sets sane defaults, see helmshim.With* for overrides.

	synth := helmshim.Synth(func(enoInputs) (map[string]any, error) {
		return map[string]any{}, nil
	})
	function.Main(synth)
}
