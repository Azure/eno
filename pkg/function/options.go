package function

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// MainOption defines an option for configuring the Main function.
type mainOption func(*mainConfig)

// MainOpts holds configuration options for the Main function.
type mainConfig struct {
	mungers []MungeFunc
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
func WithMunger(m MungeFunc) mainOption {
	return func(opts *mainConfig) {
		opts.mungers = append(opts.mungers, m)
	}
}

func WithManagedByEno() mainOption {
	return WithMunger(func(obj *unstructured.Unstructured) {
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels["app.kubernetes.io/managed-by"] = "eno"
		obj.SetLabels(labels)
	})
}

func WithReconcilationInterval(interval time.Duration) mainOption {
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
