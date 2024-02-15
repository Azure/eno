package io

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// OutputWriter allows to output resources from a synthesizer.
// Operations are thread safe.
type OutputWriter interface {
	Write(outputs ...*unstructured.Unstructured) error
	Commit() error
}

type outputWriter struct {
	w         io.Writer
	outputs   []*unstructured.Unstructured
	m         sync.Mutex
	committed bool
}

func NewOutputWriter() OutputWriter {
	return newOutputWriterToWriter(os.Stdout)
}

func newOutputWriterToWriter(w io.Writer) OutputWriter {
	return &outputWriter{
		w:         w,
		outputs:   []*unstructured.Unstructured{},
		m:         sync.Mutex{},
		committed: false,
	}
}

func (r *outputWriter) Write(outs ...*unstructured.Unstructured) error {
	r.m.Lock()
	defer r.m.Unlock()
	if r.committed {
		return ErrWriterIsCommitted
	}

	// Doing a "filter" to avoid committing nil values.
	for _, o := range outs {
		if o == nil {
			continue
		}
		r.outputs = append(r.outputs, o)
	}
	return nil
}

func (r *outputWriter) Commit() error {
	r.m.Lock()
	defer r.m.Unlock()

	rl := &krmv1.ResourceList{
		Kind:       krmv1.ResourceListKind,
		APIVersion: krmv1.SchemeGroupVersion.String(),
		Items:      r.outputs,
	}

	err := json.NewEncoder(r.w).Encode(rl)
	if err != nil {
		return errors.Join(ErrNonWriteableOutputs, err)
	}

	r.committed = true
	return nil
}
