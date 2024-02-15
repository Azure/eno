package io

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/Azure/eno/pkg/inputs"
	krmv1 "github.com/Azure/eno/pkg/krm/functions/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type InputReader interface {
	GetInput(name string) (*unstructured.Unstructured, error)
}

type inputReader struct {
	inputs []*unstructured.Unstructured
	config *unstructured.Unstructured
}

func NewInputReader() (InputReader, error) {
	return newInputReaderFromReader(os.Stdin)
}

func newInputReaderFromReader(r io.Reader) (InputReader, error) {
	rl := krmv1.ResourceList{}
	err := json.NewDecoder(r).Decode(&rl)
	if err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	return &inputReader{
		inputs: rl.Items,
		config: rl.FunctionConfig,
	}, nil
}

func (i *inputReader) GetInput(name string) (*unstructured.Unstructured, error) {
	for _, i := range i.inputs {
		if n, ok := getInputName(i); ok && n == name {
			return i, nil
		}
	}
	return nil, ErrInputNotFound
}

func getInputName(i *unstructured.Unstructured) (string, bool) {
	if i == nil {
		return "", false
	}
	annos := i.GetAnnotations()
	n, ok := annos[inputs.InputNameAnnotationKey]
	return n, ok
}
