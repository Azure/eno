# Loader Package

The `loader` package provides utilities for loading and parsing any Kubernetes objects from YAML/JSON files.

## Usage

### Basic Usage

```go
import (
    "github.com/Azure/eno/pkg/loader"
    apiv1 "k8s.io/api/core/v1"
    extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
    "k8s.io/apimachinery/pkg/runtime"
)

// Create a scheme with the types you want to load
scheme := runtime.NewScheme()

// Register standard Kubernetes types
apiv1.AddToScheme(scheme)
extv1.AddToScheme(scheme)

// Load objects from a directory
objects, err := loader.LoadObjects("/path/to/manifests", scheme)
if err != nil {
    log.Fatal(err)
}

// Process the loaded objects
for _, obj := range objects {
    switch obj := obj.(type) {
    case *extv1.CustomResourceDefinition:
        fmt.Printf("Loaded CRD: %s\n", obj.Name)
    case *apiv1.ConfigMap:
        fmt.Printf("Loaded ConfigMap: %s\n", obj.Name)
    case *apiv1.Secret:
        fmt.Printf("Loaded Secret: %s\n", obj.Name)
    default:
        fmt.Printf("Loaded object of type: %T\n", obj)
    }
}
```

### Integration with Controller-Runtime

```go
import (
    "github.com/Azure/eno/pkg/loader"
    "sigs.k8s.io/controller-runtime/pkg/manager"
)

// Use with an existing controller-runtime manager
func loadManifests(mgr manager.Manager) error {
    // Use the manager's scheme which already has your types registered
    objects, err := loader.LoadObjects("/path/to/manifests", mgr.GetScheme())
    if err != nil {
        return err
    }
    
    // Apply objects to the cluster
    for _, obj := range objects {
        if err := mgr.GetClient().Create(ctx, obj); err != nil {
            return err
        }
    }
    
    return nil
}
```

### Custom API Types

```go
import (
    "github.com/Azure/eno/pkg/loader"
    "k8s.io/apimachinery/pkg/runtime"
    
    // Your custom API types
    myapiv1 "github.com/example/myoperator/api/v1"
)

// Create a scheme with your custom types
scheme := runtime.NewScheme()

// Register your custom types
myapiv1.AddToScheme(scheme)

// Load manifests that include your custom resources
objects, err := loader.LoadObjects("/path/to/custom/manifests", scheme)
if err != nil {
    log.Fatal(err)
}
```

## Functions

### `LoadObjects(folder string, scheme *runtime.Scheme) ([]client.Object, error)`

Reads Kubernetes YAML/JSON files from the specified folder and returns a slice of `client.Object`.

**Parameters:**
- `folder`: Path to the directory containing Kubernetes manifest files
- `scheme`: Kubernetes runtime scheme with all required types registered (**required**)

**Returns:** 
- Slice of `client.Object` containing the loaded Kubernetes objects
- Error if loading, parsing, or deserialization fails

**Behavior:**
- Recursively walks through the directory
- Processes files with `.yaml`, `.yml`, and `.json` extensions
- Validates that all objects implement the `client.Object` interface
- Ignores commented sections and empty documents
- Requires all object types to be registered in the provided scheme

## Key Features

- ✅ **Generic Object Loading**: Supports any Kubernetes object type
- ✅ **Required Scheme**: Ensures type safety and proper deserialization
- ✅ **Multiple File Formats**: Handles YAML and JSON files
- ✅ **Recursive Directory Traversal**: Processes nested folder structures
- ✅ **Comprehensive Error Handling**: Clear error messages for debugging
- ✅ **Controller-Runtime Integration**: Works seamlessly with existing schemes
- ✅ **Type Validation**: Ensures loaded objects implement client.Object interface

## Differences from `pkg/crds`

| Feature | `pkg/loader` | `pkg/crds` |
|---------|-------------|------------|
| **Object Types** | Any Kubernetes object | CRDs only |
| **Scheme Parameter** | Required | Optional (with default) |
| **Type Validation** | Generic client.Object | CRD-specific validation |
| **Use Case** | General manifest loading | CRD-specific operations |

## Error Handling

The package provides detailed error messages for common issues:

- **Missing scheme**: "scheme is required"
- **Folder not found**: "folder does not exist: /path"
- **Unregistered types**: "failed to create object for GVK"
- **Invalid YAML/JSON**: "failed to decode object"
- **Type safety**: "object does not implement client.Object interface"
