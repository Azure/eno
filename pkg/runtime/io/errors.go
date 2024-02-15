package io

import "errors"

var (
	ErrInvalidInput  = errors.New("stdin did not produce a valid input")
	ErrInputNotFound = errors.New("the requested named input was not found")

	ErrWriterIsCommitted   = errors.New("cannot add new items to a writer once it's been flushed")
	ErrNonWriteableOutputs = errors.New("wat not able to write items to stdout")

	ErrPointerConversionTarget = errors.New("conversion already returns pointer to the type argument, do not provide a pointer")
	ErrConversionFailed        = errors.New("the given unstructured cannot be converted to the target concrete type")
)
