package main

import "github.com/Azure/eno/pkg/helmshim"

func main() {
	helmshim.MustRenderChart(helmshim.ParseFlags()...)
}
