package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// GetRESTConfig is a convenience method to avoid manually opening a file.
func GetRESTConfig(filename string) (*rest.Config, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("could not get read Kubeconfig file: %w", err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(b)
	if err != nil {
		return nil, fmt.Errorf("could not get get Kubeconfig from file: %w", err)
	}
	return cfg, nil
}
