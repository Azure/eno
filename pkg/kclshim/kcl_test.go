package kclshim

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestSynthesize(t *testing.T) {
	input, _ := os.Open("input.json")
	stdin, w, _ := os.Pipe()
	os.Stdin = stdin
	io.Copy(w, input)
	w.Close()

	r, stdout, _ := os.Pipe()
	os.Stdout = stdout

	pwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current working directory: %v", err)
	}
	t.Logf("Current working directory: %s", pwd)

	exitCode := Synthesize("./example_synthesizer")
	if exitCode != 0 {
		t.Errorf("Synthesize() returned non-zero exit code: %d", exitCode)
	}
	
	stdout.Close()

	buf, _ := io.ReadAll(r)
	output := string(buf)

    expected := `{
    "apiVersion":"apps/v1",
    "kind":"Deployment"
}`
	for _, whitespace := range []string{"\n", "\t", " "} {
		expected = strings.ReplaceAll(expected, whitespace, "")
		output = strings.ReplaceAll(output, whitespace, "")
	}
	if output != expected {
		t.Errorf("Expected output:\n%s\nGot:\n%s", expected, output)
	}
}
