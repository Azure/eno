// Package function provides a framework for creating Eno synthesizer functions that conform to the KRM Functions specification.
//
// Eno synthesizer functions consume input Kubernetes resources and produce output Kubernetes resources.
// Functions are defined as simple Go functions that take a struct defining the expected inputs and return a list of
// Kubernetes objects.
//
// # Basic Usage
//
// A simple function that creates a Pod:
//
//	func main() {
//	    function.Main(func(inputs struct{}) ([]client.Object, error) {
//	        pod := &corev1.Pod{}
//	        pod.Name = "example-pod"
//	        return []client.Object{pod}, nil
//	    })
//	}
//
// # Input Handling
//
// Inputs are defined as a struct with fields tagged with the `eno_key` annotation that references
// the key of the input object in the KRM ResourceList:
//
//	type MyInputs struct {
//	    ConfigMap *corev1.ConfigMap `eno_key:"my-config"`
//	    Secret    *corev1.Secret    `eno_key:"my-secret"`
//	}
//
//	function.Main(func(inputs MyInputs) ([]client.Object, error) {
//	    // Access input fields
//	    configValue := inputs.ConfigMap.Data["key"]
//	    secretValue := string(inputs.Secret.Data["key"])
//
//	    // Create and return objects
//	    // ...
//	})
//
// # Custom Input Types
//
// For more complex input handling, custom input types can be registered using AddCustomInputType:
//
//	type MyCustomType struct {
//	    Value string
//	}
//
//	function.AddCustomInputType(func(cm *corev1.ConfigMap) (*MyCustomType, error) {
//	    return &MyCustomType{
//	        Value: cm.Data["key"],
//	    }, nil
//	})
//
//	type MyInputs struct {
//	    Custom *MyCustomType `eno_key:"my-config"`
//	}
package function