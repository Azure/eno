import k8s.api.core.v1 as corev1
import k8s.apimachinery.pkg.apis.meta.v1 as metav1

service_account = corev1.ServiceAccount {
    metadata: metav1.ObjectMeta {
        name: "example-service-account",
        namespace: "default",
    }
}