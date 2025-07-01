// Package kclshim allows you to create Eno Synthesizers with KCL (https://www.kcl-lang.io/docs/reference/lang/tour).
//
// Here is how to use KCL shim.
//
// 1. Create a folder and write your KCLs. Take "./fixtures/example_synthesizer/main.k" as an example.
//
// 2. Write a main.go and call "Synthesize()", defined below.
package kclshim

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kcl "kcl-lang.io/kcl-go"
	"kcl-lang.io/kcl-go/pkg/spec/gpyrpc"
)

func printErr(err error) {
	rl := krmv1.ResourceList{
		APIVersion: krmv1.SchemeGroupVersion.String(),
		Kind:       krmv1.ResourceListKind,
		Items:      []*unstructured.Unstructured{},
		Results: []*krmv1.Result{
			{
				Message:  err.Error(),
				Severity: krmv1.ResultSeverityError,
			},
		},
	}
	bytes, err := json.Marshal(rl)
	if err != nil {
		rl.Results = append(rl.Results, &krmv1.Result{
			Message:  fmt.Sprintf("error marshaling error response: %v", err),
			Severity: krmv1.ResultSeverityError,
		})
	}
	fmt.Print(string(bytes))
}

func Synthesize(workingDir string) {
	buffer, err := io.ReadAll(os.Stdin)
	if err != nil {
		printErr(fmt.Errorf("error reading from stdin: %w", err))
		return
	}
	input := string(buffer)

	depResult, err := kcl.UpdateDependencies(&gpyrpc.UpdateDependencies_Args{
		ManifestPath: workingDir,
	})
	if err != nil {
		printErr(fmt.Errorf("error updating dependencies: %w", err))
		return
	}

	depOpt := kcl.NewOption()
	depOpt.ExternalPkgs = depResult.ExternalPkgs

	results, err := kcl.Run(
		"main.k",
		kcl.WithWorkDir(workingDir),
		*depOpt,
		kcl.WithOptions(fmt.Sprintf("input=%s", input)),
	)
	if err != nil {
		printErr(fmt.Errorf("error running KCL: %w", err))
		return
	}

	result, err := results.First().ToMap()
	output := result["output"]
	outputJSON, err := json.Marshal(output)
	if err != nil {
		printErr(fmt.Errorf("error marshaling output to JSON: %w", err))
		return
	}

	_, err = fmt.Println(string(outputJSON))
	if err != nil {
		printErr(fmt.Errorf("error printing output: %w", err))
		return
	}
}
