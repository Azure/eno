package main

import (
	"strconv"

	"github.com/Azure/eno/pkg/function"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func main() {
	w := function.NewDefaultOutputWriter()
	r, err := function.NewDefaultInputReader()
	if err != nil {
		panic(err) // non-zero exits will be retried
	}

	input := &corev1.ConfigMap{}
	function.ReadInput(r, "example-input", input)

	replicas, _ := strconv.Atoi(input.Data["replicas"])

	deploy := &appsv1.Deployment{}
	deploy.APIVersion = "apps/v1"
	deploy.Kind = "Deployment"
	deploy.Name = "example-nginx-deployment"
	deploy.Namespace = "default"
	deploy.Spec.Replicas = ptr.To(int32(replicas))
	deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nginx-example"}}
	deploy.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "nginx-example"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "nginx",
				Image: "nginx:latest",
			}},
		},
	}
	w.Add(deploy)

	w.Write()
}
