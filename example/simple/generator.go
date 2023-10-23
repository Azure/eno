package main

import (
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Azure/eno/generation"
)

func main() {
	scheme := runtime.NewScheme()
	corev1.AddToScheme(scheme)
	appsv1.AddToScheme(scheme)

	generation.MustGenerate(scheme, Generate)
}

func Generate(inputs *generation.Inputs) ([]client.Object, error) {
	test := generation.FindResource[*corev1.ConfigMap](inputs, "test-inputs")

	if test.Data["enable"] == "false" {
		return nil, nil
	}

	count, err := strconv.Atoi(test.Data["replicas"])
	if err != nil {
		return nil, err
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: test.Data["namespace"],
		},
	}

	objects := []client.Object{ns}
	for i := 0; i < count; i++ {
		objects = append(objects, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("test-%d", i),
				Namespace: ns.Name,
			},
			Data: map[string]string{
				"empty": string(make([]byte, 1024)),
			},
		})
	}

	return objects, nil
}
