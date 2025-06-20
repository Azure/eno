package kclshim

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	kcl "kcl-lang.io/kcl-go"
    "kcl-lang.io/kcl-go/pkg/spec/gpyrpc"
)

func Synthesize(workingDir string) int {
	buffer, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error reading from stdin:", err)
		return 1
	}
	input_json := string(buffer)
	if strings.Contains(input_json, "\"\"\"") {
		fmt.Fprintln(os.Stderr, "Input JSON contains \"\"\", which are not allowed.")
		return 2
	}

// 	code := fmt.Sprintf(`
// import json
// import pkg

// input_json = """%s"""
// input = json.decode(input_json)
// output = pkg.Synthesize(input)
// `, input_json)

	 depResult, err := kcl.UpdateDependencies(&gpyrpc.UpdateDependencies_Args{
        ManifestPath: workingDir,
    })
    if err != nil {
        fmt.Fprintln(os.Stderr, "Error updating dependencies:", err)
		return 3
    }

    depOpt := kcl.NewOption()
    depOpt.ExternalPkgs = depResult.ExternalPkgs

	results, err := kcl.Run(
		"main.k",
		//kcl.WithCode(code),
		kcl.WithWorkDir(workingDir),
		*depOpt,
		kcl.WithOptions("kind=deployment"),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error running KCL:", err)
		return 4
	}

	result, err := results.First().ToMap()
	output := result["output"]
	outputJSON, err := json.Marshal(output)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error marshaling output to JSON:", err)
		return 6
	}
	
	_, err = fmt.Println(string(outputJSON))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error printing output:", err)
		return 5
	}
	return 0
}
