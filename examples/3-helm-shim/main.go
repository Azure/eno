package main

import (
	"github.com/Azure/eno/pkg/helmshim"
)

func main() {
	// The Helm shim sets sane defaults, see helmshim.With* for overrides.
	//
	// WithValuesFunc is particularly useful in cases where there is not a 1:1 mapping
	// between input resources and Helm values.
	helmshim.MustRenderChart()
}
