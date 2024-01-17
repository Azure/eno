package k8s

import (
	"fmt"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func GetRESTConfigOrDie(filename string) *rest.Config {
	b, err := os.ReadFile(filename)
	if err != nil {
		fmt.Printf("Could not get read Kubeconfig file: %s", err.Error())
		os.Exit(1)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(b)
	if err != nil {
		fmt.Printf("Could not get get Kubeconfig from file: %s", err.Error())
		os.Exit(1)
	}
	return cfg
}
