package function

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type OutputWriter struct {
	outputs   []*unstructured.Unstructured
	io        io.Writer
	committed bool
}

func NewDefaultOutputWriter() *OutputWriter {
	return NewOutputWriter(os.Stdout)
}

func NewOutputWriter(w io.Writer) *OutputWriter {
	return &OutputWriter{
		outputs:   []*unstructured.Unstructured{},
		io:        w,
		committed: false,
	}
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
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return fmt.Errorf(
				"converting %s %s to unstructured: %w",
				o.GetName(),
				o.GetObjectKind().GroupVersionKind().Kind,
				err,
			)
		}
		w.outputs = append(w.outputs, &unstructured.Unstructured{Object: obj})
	}
	return nil
}

func (w *OutputWriter) Write() error {
	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
		Items:      w.outputs,
	}

	err := json.NewEncoder(w.io).Encode(rl)
	if err != nil {
		return fmt.Errorf("writing output to stdou: %w", err)
	}

	w.committed = true
	return nil
}
