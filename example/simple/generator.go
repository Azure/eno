package main

import (
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

	replicasInt, err := strconv.Atoi(test.Data["replicas"])
	if err != nil {
		return nil, err
	}
	replicas := int32(replicasInt)

	podLabels := map[string]string{"app": "nginx"}

	output := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "nginx",
			Namespace:   "default",
			Annotations: test.Data, // pass through the input
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "nginx",
						Image: "nginx:latest",
					}},
				},
			},
		},
	}

	return []client.Object{output}, nil
}
