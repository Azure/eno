# CRDs Package

The `crds` package provides utilities for loading and parsing Kubernetes Custom Resource Definitions (CRDs) from YAML files.

## Usage

```go
import "github.com/Azure/eno/pkg/crds"

// Load CRDs from a directory
objects, err := crds.Load("/path/to/crds/directory", nil)
if err != nil {
    log.Fatal(err)
}

// Use the loaded CRDs
for _, obj := range objects {
    crd := obj.(*apiextensionsv1.CustomResourceDefinition)
    fmt.Printf("Loaded CRD: %s\n", crd.Name)
}
```

## Functions

### `Load(crdsFolder string, scheme *runtime.Scheme) ([]client.Object, error)`

Reads CRD YAML files from the specified folder, decodes them, and returns a slice of `client.Object`.

- `crdsFolder`: Path to the directory containing CRD YAML files
- `scheme`: Kubernetes runtime scheme. If nil, a default scheme with apiextensions/v1 will be created
- Returns: Slice of client.Object containing the loaded CRDs

The function recursively walks through the directory and processes all files, automatically filtering for YAML content.

## Features

- Recursive directory traversal
- Automatic YAML/JSON detection and parsing
- Built-in CRD validation
- Support for custom Kubernetes runtime schemes
- Comprehensive error handling
- Ignores commented sections in YAML files
