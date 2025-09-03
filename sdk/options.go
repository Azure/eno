package function

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// option defines an option for configuring/augmenting the Main function.
type Option func(*mainConfig)

// mainConfig holds configuration options for the Main function.
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
// If no scheme is provided, or if an object's type is not registered in the scheme,
// the object will be processed without modification.
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

// WithManagedByEno returns an iption that annotates the given Kubernetes object to indicate
// that it is managed by Eno. It sets the "eno.azure.io/managed-by" annotation to the Eno controller identifier.
func WithManagedByEno() Option {
	return WithMunger(func(obj *unstructured.Unstructured) {
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["app.kubernetes.io/managed-by"] = "Eno"
		obj.SetLabels(labels)
	})
}

// WithReconcilationInterval returns an option that annotates the given Kubernetes object to configure
// its reconciliation interval. It sets the "eno.azure.io/reconcile-interval" annotation to the provided
// duration string, which controls how frequently Eno will reconcile the resource.
func WithReconcilationInterval(interval time.Duration) Option {
	return WithMunger(func(obj *unstructured.Unstructured) {
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["eno.azure.io/reconcile-interval"] = interval.String()
		obj.SetAnnotations(annotations)
	})
}

// CompositeMungeFunc creates a composite munge function that applies all
// mungers in sequence. Returns nil if no mungers are configured.
func (opts *mainConfig) CompositeMungeFunc() MungeFunc {
	if len(opts.mungers) == 0 {
		return nil
	}

	return func(obj *unstructured.Unstructured) {
		for _, munger := range opts.mungers {
			munger(obj)
		}
	}
}
