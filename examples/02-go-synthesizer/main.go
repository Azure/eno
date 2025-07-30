package main

import (
	"strconv"

	"github.com/Azure/eno/pkg/function"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Inputs struct {
	Config *corev1.ConfigMap `eno_key:"example-input"`
}

func synthesize(inputs Inputs) ([]client.Object, error) {
	replicas, _ := strconv.ParseInt(inputs.Config.Data["replicas"], 10, 32)
	replicas := int32(replicas64)

	deploy := &appsv1.Deployment{}
	deploy.APIVersion = "apps/v1"
	deploy.Kind = "Deployment"
	deploy.Name = "example-nginx-deployment"
	deploy.Namespace = "default"
	deploy.Spec.Replicas = ptr.To(replicas)
	deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nginx-example"}}
	deploy.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"app": "nginx-example"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "nginx",
				Image: "nginx:latest",
				Ports: []corev1.ContainerPort{{ContainerPort: 80}},
			}},
		},
	}

	return []client.Object{deploy}, nil
}

func main() {
	function.Main(synthesize)
}
