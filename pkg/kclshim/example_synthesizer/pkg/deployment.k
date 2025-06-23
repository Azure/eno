import k8s.api.apps.v1 as appsv1
import k8s.api.core.v1 as corev1
import k8s.apimachinery.pkg.apis.meta.v1 as metav1

GetDeployment = lambda mcr_base: str -> appsv1.Deployment {
    appsv1.Deployment {
        metadata: metav1.ObjectMeta {
            name: "my-deployment",
            namespace: "default",
        },
        spec: appsv1.DeploymentSpec {
            replicas: 3,
            selector: metav1.LabelSelector {
                matchLabels: {"app": "my-app"},
            },
            template: corev1.PodTemplateSpec {
                metadata: metav1.ObjectMeta {
                    labels: {"app": "my-app"},
                },
                spec: corev1.PodSpec {
                    containers: [
                        corev1.Container {
                            name: "my-container",
                            image: mcr_base + "/a/b/my-image:latest",
                        },
                    ],
                },
            },
        },
    }
}