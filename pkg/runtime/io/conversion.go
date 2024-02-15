package io

import (
	"errors"
	"reflect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// Convert is a utility function that wraps the default unstructured converter.
func Convert[T any](u *unstructured.Unstructured) (*T, error) {
	var res T
	_T := reflect.TypeOf(res)
	if _T.Kind() == reflect.Pointer {
		return nil, ErrPointerConversionTarget
	}

	err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &res)
	if err != nil {
		return nil, errors.Join(ErrConversionFailed, err)
	}
	return &res, nil
}
