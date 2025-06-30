package kclshim

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestSynthesize(t *testing.T) {
	tests := []struct {
		name           string
		workingDir     string
		expectedOutput string
	}{
		{
			name:       "successful synthesis",
			workingDir: "fixtures/example_synthesizer",
			expectedOutput: `{
                "apiVersion":"config.kubernetes.io/v1",
                "items":[
                    {
                        "apiVersion": "apps/v1",
                        "kind": "Deployment",
                        "metadata": {
                            "name": "my-deployment",
                            "namespace": "default"
                        },
                        "spec": {
                            "replicas": 3,
                            "selector": {
                                "matchLabels": {
                                    "app": "my-app"
                                }
                            },
                            "template": {
                                "metadata": {
                                    "labels": {
                                        "app": "my-app"
                                    }
                                },
                                "spec": {
                                    "containers": [
                                        {
                                            "image": "mcr.microsoft.com/a/b/my-image:latest",
                                            "name": "my-container"
                                        }
                                    ]
                                }
                            }
                        }
                    },
                    {
                        "apiVersion": "v1",
                        "kind": "ServiceAccount",
                        "metadata": {
                            "name": "my-service-account",
                            "namespace": "default"
                        }
                    }
                ],
                "kind": "ResourceList"
            }`,
		},
		{
			name: "failed synthesis",
			workingDir: "fixtures/bad_example_synthesizer",
			expectedOutput: `{
				"apiVersion":"config.kubernetes.io/v1",
				"kind":"ResourceList",
				"items":[],
				"results":[
					{
						"message":"error updating dependencies: No such file or directory (os error 2)",
						"severity":"error"
					}
				]
			}`,	
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalStdin := os.Stdin
			originalStdout := os.Stdout
			defer func() {
				os.Stdin = originalStdin
				os.Stdout = originalStdout
			}()

			input, err := os.Open("fixtures/example_input.json")
			if err != nil {
				t.Fatalf("Failed to open input file: %v", err)
			}
			defer input.Close()

			stdin, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("Failed to create stdin pipe: %v", err)
			}
			os.Stdin = stdin

			go func() {
				defer w.Close()
				io.Copy(w, input)
			}()

			r, stdout, err := os.Pipe()
			if err != nil {
				t.Fatalf("Failed to create stdout pipe: %v", err)
			}
			os.Stdout = stdout

			Synthesize(tt.workingDir)
			stdout.Close()

			buf, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("Failed to read output: %v", err)
			}
			output := string(buf)

			normalizedExpected := normalizeWhitespace(tt.expectedOutput)
			normalizedOutput := normalizeWhitespace(output)

			if normalizedOutput != normalizedExpected {
				t.Errorf("Output mismatch\nExpected:\n%s\nGot:\n%s", normalizedExpected, normalizedOutput)
			}
		})
	}
}

func normalizeWhitespace(s string) string {
	for _, whitespace := range []string{"\n", "\t", " "} {
		s = strings.ReplaceAll(s, whitespace, "")
	}
	return s
}
