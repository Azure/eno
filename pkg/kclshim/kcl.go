// Package kclshim allows you to create Eno Synthesizers with KCL (https://www.kcl-lang.io/docs/reference/lang/tour).
//
// Here is how to use KCL shim.
//
// 1. Create a folder and write your KCLs. Take "./fixtures/example_synthesizer/main.k" as an example.
//
// 2. Write a main.go and call "Synthesize(workingDir, input)", defined below.
package kclshim

import (
	"encoding/json"
	"fmt"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	kcl "kcl-lang.io/kcl-go"
	"kcl-lang.io/kcl-go/pkg/spec/gpyrpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Synthesize runs a KCL program in the given directory with JSON-serializable structured input.
// It returns the synthesized Kubernetes objects or an error.
func Synthesize(workingDir string, input any) ([]client.Object, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("error marshaling input to JSON: %w", err)
	}
	inputStr := string(inputJSON)

	depResult, err := kcl.UpdateDependencies(&gpyrpc.UpdateDependencies_Args{
		ManifestPath: workingDir,
	})
	if err != nil {
		return nil, fmt.Errorf("error updating dependencies: %w", err)
	}

	depOpt := kcl.NewOption()
	depOpt.ExternalPkgs = depResult.ExternalPkgs

	results, err := kcl.Run(
		"main.k",
		kcl.WithWorkDir(workingDir),
		*depOpt,
		kcl.WithOptions(fmt.Sprintf("input=%s", inputStr)),
	)
	if err != nil {
		return nil, fmt.Errorf("error running KCL: %w", err)
	}

	result, err := results.First().ToMap()
	if err != nil {
		return nil, fmt.Errorf("error converting KCL result to map: %w", err)
	}

	output := result["output"]
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return nil, fmt.Errorf("error marshaling output to JSON: %w", err)
	}

	var rl krmv1.ResourceList
	if err := json.Unmarshal(outputJSON, &rl); err != nil {
		return nil, fmt.Errorf("error unmarshaling output to ResourceList: %w", err)
	}

	var objects []client.Object
	for _, item := range rl.Items {
		objects = append(objects, item)
	}
	return objects, nil
}
