package sdk

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type Option func(*mainConfig)

type MungeFunc func(*unstructured.Unstructured)

type mainConfig struct {
	mungers []MungeFunc
	scheme  *runtime.Scheme
}

// WithScheme configures the runtime.Scheme used for automatic type metadata inference.
//
// The scheme enables automatic population of TypeMeta (apiVersion and kind) fields
// for Kubernetes objects based on their Go types. When an object is processed and
// its GroupVersionKind is empty, the scheme will be consulted to determine the
// appropriate apiVersion and kind values.
//
// If no scheme is provided, the default kubectl scheme will be used.
//
// Example usage:
//
//	scheme := runtime.NewScheme()
//	corev1.AddToScheme(scheme)
//	appsv1.AddToScheme(scheme)
//	Main(synthesizer, WithScheme(scheme))
func WithScheme(scheme *runtime.Scheme) Option {
	return func(mc *mainConfig) {
		mc.scheme = scheme
	}
}

// WithMunger adds a munge function that will be applied to each output object.
// Multiple munge functions can be provided and they will be applied in order.
//
// Example usage:
//
//	Main(synthesizer,
//		WithMunger(func(obj *unstructured.Unstructured) {
//			// Add common labels
//			labels := obj.GetLabels()
//			if labels == nil {
//				labels = make(map[string]string)
//			}
//			labels["app.kubernetes.io/managed-by"] = "eno"
//			obj.SetLabels(labels)
//		}),
//		WithMunger(func(obj *unstructured.Unstructured) {
//			// Add environment-specific annotations
//			annotations := obj.GetAnnotations()
//			if annotations == nil {
//				annotations = make(map[string]string)
//			}
//			annotations["eno.azure.io/reconcile-interval"] = "1m"
//			obj.SetAnnotations(annotations)
//		}),
//	)
func WithMunger(m MungeFunc) Option {
	return func(opts *mainConfig) {
		opts.mungers = append(opts.mungers, m)
	}
}
