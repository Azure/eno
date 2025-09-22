package stdlib

import (
	"time"

	"github.com/Azure/eno/sdk"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TODO: add tests

// WithReconcilationInterval returns an option that annotates the given Kubernetes object to configure
// its reconciliation interval. It sets the "eno.azure.io/reconcile-interval" annotation to the provided
// duration string, which controls how frequently Eno will reconcile the resource.
func WithReconcilationInterval(interval time.Duration) sdk.Option {
	return sdk.WithMunger(func(obj *unstructured.Unstructured) {
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["eno.azure.io/reconcile-interval"] = interval.String()
		obj.SetAnnotations(annotations)
	})
}

// WithManagedByEno returns an iption that annotates the given Kubernetes object to indicate
// that it is managed by Eno. It sets the "eno.azure.io/managed-by" annotation to the Eno controller identifier.
func WithManagedByEno() sdk.Option {
	return sdk.WithMunger(func(obj *unstructured.Unstructured) {
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["app.kubernetes.io/managed-by"] = "Eno"
		obj.SetLabels(labels)
	})
}
