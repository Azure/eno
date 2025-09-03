package function

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

func (w *OutputWriter) Add(outs ...*unstructured.Unstructured) error {
	if w.committed {
		return fmt.Errorf("cannot add to a committed output")
	}

	// Doing a "filter" to avoid committing nil values.
	for _, o := range outs {
		w.outputs = append(w.outputs, o)
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
