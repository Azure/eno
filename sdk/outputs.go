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

var Scheme = scheme.Scheme

type OutputWriter struct {
	outputs   []*unstructured.Unstructured
	results   []*krmv1.Result
	io        io.Writer
	committed bool
	munge     MungeFunc
}

type MungeFunc func(*unstructured.Unstructured)

func NewDefaultOutputWriter() *OutputWriter {
	return NewOutputWriter(os.Stdout, nil)
}

func NewOutputWriter(w io.Writer, munge MungeFunc) *OutputWriter {
	return &OutputWriter{
		outputs:   []*unstructured.Unstructured{},
		io:        w,
		committed: false,
		munge:     munge,
	}
}

func (w *OutputWriter) AddResult(result *krmv1.Result) {
	w.results = append(w.results, result)
}

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
