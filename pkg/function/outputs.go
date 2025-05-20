package function

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Scheme is the default Kubernetes scheme used for serialization and GVK resolution.
var Scheme = scheme.Scheme

// OutputWriter handles writing Kubernetes resources as KRM function output.
// It manages the collection of resources and results that will be written as a
// KRM ResourceList when Write() is called.
type OutputWriter struct {
	outputs   []*unstructured.Unstructured
	results   []*krmv1.Result
	io        io.Writer
	committed bool
	munge     MungeFunc
}

// MungeFunc is a function that can modify a Kubernetes object before it is added to the output.
type MungeFunc func(*unstructured.Unstructured)

// NewDefaultOutputWriter creates an OutputWriter that writes to os.Stdout.
func NewDefaultOutputWriter() *OutputWriter {
	return NewOutputWriter(os.Stdout, nil)
}

// NewOutputWriter creates an OutputWriter that writes to the specified writer.
// The munge function is called on each object before it is added to the output,
// allowing for customization of the objects.
func NewOutputWriter(w io.Writer, munge MungeFunc) *OutputWriter {
	return &OutputWriter{
		outputs:   []*unstructured.Unstructured{},
		io:        w,
		committed: false,
		munge:     munge,
	}
}

// AddResult adds a Result to the output ResourceList, typically used for reporting errors.
func (w *OutputWriter) AddResult(result *krmv1.Result) {
	w.results = append(w.results, result)
}

// Add adds one or more Kubernetes objects to the output ResourceList.
// Objects are converted to unstructured.Unstructured and added to the list of outputs.
// Returns an error if the output has already been written or if there's an issue
// converting the objects to unstructured format.
func (w *OutputWriter) Add(outs ...client.Object) error {
	if w.committed {
		return fmt.Errorf("cannot add to a committed output")
	}

	// Doing a "filter" to avoid committing nil values.
	for _, o := range outs {
		if o == nil {
			continue
		}

		// Resolve GVK if needed
		if o.GetObjectKind().GroupVersionKind().Empty() {
			gvks, _, err := Scheme.ObjectKinds(o)
			if err != nil || len(gvks) == 0 {
				return fmt.Errorf("unable to determine GVK for object %s: %w", o.GetName(), err)
			}
			o.GetObjectKind().SetGroupVersionKind(gvks[0])
		}

		// Encode
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return fmt.Errorf(
				"converting %s %s to unstructured: %w",
				o.GetName(),
				o.GetObjectKind().GroupVersionKind().Kind,
				err,
			)
		}
		u := &unstructured.Unstructured{Object: obj}
		if w.munge != nil {
			w.munge(u)
		}
		w.outputs = append(w.outputs, u)
	}
	return nil
}

// Write serializes the collected objects and results as a KRM ResourceList and writes
// it to the configured io.Writer. After calling Write, the OutputWriter is marked as
// committed and further calls to Add will return an error.
func (w *OutputWriter) Write() error {
	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
		Items:      w.outputs,
		Results:    w.results,
	}

	err := json.NewEncoder(w.io).Encode(rl)
	if err != nil {
		return fmt.Errorf("writing output to stdou: %w", err)
	}

	w.committed = true
	return nil
}
